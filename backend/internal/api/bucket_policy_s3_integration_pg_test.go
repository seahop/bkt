package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/middleware"
	"bkt/internal/models"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Bucket policies over the S3 API (GET/PUT/DELETE ?policy, ?policyStatus)
// and the explicit routing of every other bucket sub-resource, driven
// through the real S3 router (SigV4 middleware + routes).

type bpFixture struct {
	cfg              *config.Config
	s3               *gin.Engine
	admin, user, rdr models.User
	adminKey         models.AccessKey
	userKey          models.AccessKey
	rdrKey           models.AccessKey
	nobodyKey        models.AccessKey
	bucket, pub      models.Bucket
}

// grantPolicy attaches a user policy with the given statements JSON.
func grantPolicy(t *testing.T, u models.User, statements string) {
	t.Helper()
	p := models.Policy{Name: itName("pol"), Document: `{"Version":"2012-10-17","Statement":` + statements + `}`}
	if err := database.DB.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&u).Association("Policies").Append(&p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.DB.Exec(`DELETE FROM user_policies WHERE policy_id = ?`, p.ID)
		database.DB.Exec(`DELETE FROM policies WHERE id = ?`, p.ID)
	})
}

func newBPFixture(t *testing.T) *bpFixture {
	t.Helper()
	cfg := itConfig(t)
	f := &bpFixture{cfg: cfg}
	f.admin = mkUser(t, itName("bpadm"), "password-123", true, false)
	f.user = mkUser(t, itName("bpusr"), "password-123", false, false)
	f.rdr = mkUser(t, itName("bprdr"), "password-123", false, false)
	nobody := mkUser(t, itName("bpnob"), "password-123", false, false)
	f.bucket = itBucket(t, f.admin.ID, nil)
	f.pub = itBucket(t, f.admin.ID, func(b *models.Bucket) { b.IsPublic = true })
	t.Cleanup(func() {
		database.DB.Exec(`DELETE FROM audit_logs WHERE resource_id IN (?, ?)`, f.bucket.ID.String(), f.pub.ID.String())
	})

	// user: read/list the bucket. rdr: additionally s3:GetBucketPolicy.
	grantPolicy(t, f.user, fmt.Sprintf(`[{"Effect":"Allow","Action":["s3:GetObject","s3:ListBucket"],"Resource":["arn:aws:s3:::%[1]s","arn:aws:s3:::%[1]s/*"]}]`, f.bucket.Name))
	grantPolicy(t, f.rdr, fmt.Sprintf(`[{"Effect":"Allow","Action":["s3:GetBucketPolicy"],"Resource":["arn:aws:s3:::%[1]s"]}]`, f.bucket.Name))

	up := itS3Router(cfg, f.admin)
	s3PutString(t, up, f.bucket.Name, "secret/x", "top secret")
	s3PutString(t, up, f.bucket.Name, "public.txt", "hello")

	f.adminKey = itAccessKey(t, f.admin, "bpa-"+uuid.NewString()[:6], nil)
	f.userKey = itAccessKey(t, f.user, "bpu-"+uuid.NewString()[:6], nil)
	f.rdrKey = itAccessKey(t, f.rdr, "bpr-"+uuid.NewString()[:6], nil)
	f.nobodyKey = itAccessKey(t, nobody, "bpn-"+uuid.NewString()[:6], nil)
	f.s3 = SetupS3Router(cfg)
	return f
}

// s3Signed sends a SigV4 header-signed request with a hex payload hash (so
// the middleware verifies the body digest) to handler.
func s3Signed(t *testing.T, handler http.Handler, key models.AccessKey, method, target string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	req.Header.Set("X-Amz-Content-Sha256", hash)
	creds := aws.Credentials{AccessKeyID: key.AccessKey, SecretAccessKey: "secret-" + key.Name}
	if err := v4.NewSigner().SignHTTP(context.Background(), creds, req, hash, "s3", "us-east-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func (f *bpFixture) do(t *testing.T, key models.AccessKey, method, target string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	return s3Signed(t, f.s3, key, method, target, body)
}

func expectS3Err(t *testing.T, what string, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Errorf("%s: status %d, want %d (%s)", what, w.Code, status, w.Body.String())
		return
	}
	if got := itS3ErrCode(t, w); got != code {
		t.Errorf("%s: code %q, want %q (%s)", what, got, code, w.Body.String())
	}
}

// sameJSON reports whether two documents are the same JSON value. Policies
// live in a jsonb column, so PostgreSQL normalizes whitespace and key order;
// the content round-trips, the formatting does not (AWS reformats too).
func sameJSON(a, b string) bool {
	var x, y interface{}
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

func auditCount(t *testing.T, action, resourceID string) int64 {
	t.Helper()
	var n int64
	database.DB.Model(&models.AuditLog{}).Where("action = ? AND resource_id = ?", action, resourceID).Count(&n)
	return n
}

func TestIntegrationS3BucketPolicyRoundTrip(t *testing.T) {
	f := newBPFixture(t)
	b := "/" + f.bucket.Name

	// No policy yet.
	expectS3Err(t, "GET ?policy (none)", f.do(t, f.adminKey, http.MethodGet, b+"?policy", nil), http.StatusNotFound, "NoSuchBucketPolicy")
	expectS3Err(t, "GET ?policy unknown bucket", f.do(t, f.adminKey, http.MethodGet, "/"+itName("nob")+"?policy", nil), http.StatusNotFound, "NoSuchBucket")
	if w := f.do(t, f.userKey, http.MethodGet, b+"/secret/x", nil); w.Code != http.StatusOK {
		t.Fatalf("user GET secret/x before policy: %d", w.Code)
	}

	// An AWS-shaped document (object Principal, string Action/Resource,
	// indentation) is accepted, stored and returned (as the same JSON).
	doc := fmt.Sprintf(`{
  "Version": "2012-10-17",
  "Id": "bkt-test",
  "Statement": [
    {
      "Sid": "DenySecret",
      "Effect": "Deny",
      "Principal": {"AWS": "*"},
      "Action": "s3:GetObject",
      "Resource": "arn:aws:s3:::%s/secret/*"
    }
  ]
}
`, f.bucket.Name)
	if w := f.do(t, f.adminKey, http.MethodPut, b+"?policy", []byte(doc)); w.Code != http.StatusNoContent {
		t.Fatalf("PUT ?policy: %d %s", w.Code, w.Body.String())
	}
	w := f.do(t, f.adminKey, http.MethodGet, b+"?policy", nil)
	if w.Code != http.StatusOK || !sameJSON(w.Body.String(), doc) || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("GET ?policy: %d %q (%s), want the stored document", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	if n := auditCount(t, "bucket.policy.set", f.bucket.ID.String()); n != 1 {
		t.Errorf("bucket.policy.set audit entries: %d, want 1", n)
	}

	// The policy takes effect: Deny for everyone on secret/*.
	expectS3Err(t, "user GET secret/x", f.do(t, f.userKey, http.MethodGet, b+"/secret/x", nil), http.StatusForbidden, "AccessDenied")
	if w := f.do(t, f.userKey, http.MethodGet, b+"/public.txt", nil); w.Code != http.StatusOK {
		t.Errorf("user GET public.txt: %d", w.Code)
	}

	// Who may read it: admin, or s3:GetBucketPolicy — not mere list/read access.
	if w := f.do(t, f.rdrKey, http.MethodGet, b+"?policy", nil); w.Code != http.StatusOK || !sameJSON(w.Body.String(), doc) {
		t.Errorf("GetBucketPolicy grantee GET ?policy: %d %s", w.Code, w.Body.String())
	}
	expectS3Err(t, "user GET ?policy", f.do(t, f.userKey, http.MethodGet, b+"?policy", nil), http.StatusForbidden, "AccessDenied")
	expectS3Err(t, "nobody GET ?policy", f.do(t, f.nobodyKey, http.MethodGet, b+"?policy", nil), http.StatusForbidden, "AccessDenied")

	// Writes are admin only.
	other := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":["s3:*"],"Resource":["arn:aws:s3:::%[1]s","arn:aws:s3:::%[1]s/*"]}]}`, f.bucket.Name)
	for _, k := range []models.AccessKey{f.userKey, f.rdrKey} {
		expectS3Err(t, "non-admin PUT ?policy", f.do(t, k, http.MethodPut, b+"?policy", []byte(other)), http.StatusForbidden, "AccessDenied")
		expectS3Err(t, "non-admin DELETE ?policy", f.do(t, k, http.MethodDelete, b+"?policy", nil), http.StatusForbidden, "AccessDenied")
	}

	// Invalid documents: 400 MalformedPolicy with the validator's message; the
	// stored policy is unchanged.
	for name, c := range map[string]struct{ body, msg string }{
		"condition": {fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::%s/*","Condition":{"Bool":{"aws:SecureTransport":"true"}}}]}`, f.bucket.Name), "Condition is not supported yet"},
		"not json":  {`{"Version":`, "invalid JSON"},
		"empty":     {``, "invalid JSON"},
		"service":   {`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"s3.amazonaws.com"},"Action":"s3:GetObject","Resource":"*"}]}`, "supported forms"},
		"role arn":  {`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123456789012:role/x"},"Action":"s3:GetObject","Resource":"*"}]}`, "supported forms"},
		"unknown":   {`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*","Foo":1}]}`, "unsupported policy element"},
		"> 10 KB":   {`{"Version":"2012-10-17","Statement":[{"Sid":"` + strings.Repeat("a", 11000) + `","Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`, "too large"},
	} {
		w := f.do(t, f.adminKey, http.MethodPut, b+"?policy", []byte(c.body))
		expectS3Err(t, "PUT ?policy "+name, w, http.StatusBadRequest, "MalformedPolicy")
		if !strings.Contains(w.Body.String(), c.msg) {
			t.Errorf("PUT ?policy %s: message %s, want it to mention %q", name, w.Body.String(), c.msg)
		}
	}
	big := `{"Version":"2012-10-17","Statement":[{"Sid":"` + strings.Repeat("a", 21*1024) + `","Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`
	expectS3Err(t, "PUT ?policy > 20 KB", f.do(t, f.adminKey, http.MethodPut, b+"?policy", []byte(big)), http.StatusBadRequest, "MaxMessageLengthExceeded")
	if w := f.do(t, f.adminKey, http.MethodGet, b+"?policy", nil); !sameJSON(w.Body.String(), doc) {
		t.Fatalf("stored policy changed by rejected writes: %s", w.Body.String())
	}

	// A body that does not match the signed payload hash is rejected.
	req := httptest.NewRequest(http.MethodPut, b+"?policy", strings.NewReader(other))
	sum := sha256.Sum256([]byte(doc))
	req.Header.Set("X-Amz-Content-Sha256", hex.EncodeToString(sum[:]))
	creds := aws.Credentials{AccessKeyID: f.adminKey.AccessKey, SecretAccessKey: "secret-" + f.adminKey.Name}
	if err := v4.NewSigner().SignHTTP(context.Background(), creds, req, hex.EncodeToString(sum[:]), "s3", "us-east-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	tw := httptest.NewRecorder()
	f.s3.ServeHTTP(tw, req)
	if tw.Code != http.StatusBadRequest {
		t.Errorf("PUT ?policy with a mismatched payload hash: %d %s, want 400", tw.Code, tw.Body.String())
	}
	if w := f.do(t, f.adminKey, http.MethodGet, b+"?policy", nil); !sameJSON(w.Body.String(), doc) {
		t.Fatalf("stored policy changed by an unverified body: %s", w.Body.String())
	}

	// Delete: 204, idempotent, via /bucket and /bucket/; access restored.
	if w := f.do(t, f.adminKey, http.MethodDelete, b+"?policy", nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE ?policy: %d %s", w.Code, w.Body.String())
	}
	if w := f.do(t, f.adminKey, http.MethodDelete, b+"/?policy", nil); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE /?policy again: %d %s", w.Code, w.Body.String())
	}
	expectS3Err(t, "GET ?policy after delete", f.do(t, f.adminKey, http.MethodGet, b+"?policy", nil), http.StatusNotFound, "NoSuchBucketPolicy")
	if w := f.do(t, f.userKey, http.MethodGet, b+"/secret/x", nil); w.Code != http.StatusOK {
		t.Errorf("user GET secret/x after delete: %d", w.Code)
	}
	if n := auditCount(t, "bucket.policy.delete", f.bucket.ID.String()); n != 1 {
		t.Errorf("bucket.policy.delete audit entries: %d, want 1 (the no-op delete is not audited)", n)
	}
	var entry models.AuditLog
	database.DB.Where("action = ? AND resource_id = ?", "bucket.policy.delete", f.bucket.ID.String()).First(&entry)
	var meta map[string]interface{}
	_ = json.Unmarshal([]byte(entry.Metadata), &meta)
	if entry.UserID != f.admin.ID || entry.Username != f.admin.Username || meta["via"] != "s3" || !sameJSON(fmt.Sprint(meta["previous_policy"]), doc) {
		t.Errorf("delete audit entry: user=%s/%s meta=%v", entry.UserID, entry.Username, meta)
	}
}

func TestIntegrationS3BucketPolicyAWSPrincipals(t *testing.T) {
	f := newBPFixture(t)
	b := "/" + f.bucket.Name

	cases := []struct {
		name, principal string
		userDenied      bool
	}{
		{"AWS star", `{"AWS":"*"}`, true},
		{"AWS star array", `{"AWS":["*"]}`, true},
		{"AWS username", `{"AWS":"` + f.user.Username + `"}`, true},
		{"AWS user ARN", `{"AWS":["arn:aws:iam::123456789012:user/` + f.user.Username + `"]}`, true},
		{"AWS other user ARN", `{"AWS":["arn:aws:iam::123456789012:user/someone-else"]}`, false},
		{"plain username array", `["` + f.user.Username + `"]`, true},
		{"plain star", `"*"`, true},
	}
	for _, c := range cases {
		doc := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":%s,"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/public.txt"]}]}`, c.principal, f.bucket.Name)
		if w := f.do(t, f.adminKey, http.MethodPut, b+"?policy", []byte(doc)); w.Code != http.StatusNoContent {
			t.Errorf("%s: PUT: %d %s", c.name, w.Code, w.Body.String())
			continue
		}
		if w := f.do(t, f.adminKey, http.MethodGet, b+"?policy", nil); !sameJSON(w.Body.String(), doc) {
			t.Errorf("%s: round trip: %s", c.name, w.Body.String())
		}
		w := f.do(t, f.userKey, http.MethodGet, b+"/public.txt", nil)
		if denied := w.Code == http.StatusForbidden; denied != c.userDenied {
			t.Errorf("%s: user GET public.txt: %d, want denied=%v", c.name, w.Code, c.userDenied)
		}
		// Admins bypass policies.
		if w := f.do(t, f.adminKey, http.MethodGet, b+"/public.txt", nil); w.Code != http.StatusOK {
			t.Errorf("%s: admin GET: %d", c.name, w.Code)
		}
	}
}

// Every well-known bucket sub-resource gets its documented answer — never a
// ListBucketResult — and anything unknown is 501.
func TestIntegrationS3BucketSubresourceRouting(t *testing.T) {
	f := newBPFixture(t)
	b := "/" + f.bucket.Name
	notListing := func(what string, w *httptest.ResponseRecorder) {
		t.Helper()
		if strings.Contains(w.Body.String(), "<ListBucketResult") {
			t.Errorf("%s: answered with a listing: %s", what, w.Body.String())
		}
	}
	rootElem := func(w *httptest.ResponseRecorder) string {
		var v struct{ XMLName xml.Name }
		_ = xml.Unmarshal(w.Body.Bytes(), &v)
		return v.XMLName.Local
	}

	type getCase struct {
		query  string
		status int
		code   string // S3 error code, or the root element for 200s
	}
	gets := []getCase{
		{"location", 200, "LocationConstraint"},
		{"versioning", 200, "VersioningConfiguration"},
		{"acl", 200, "AccessControlPolicy"},
		{"policyStatus", 200, "PolicyStatus"},
		{"logging", 200, "BucketLoggingStatus"},
		{"notification", 200, "NotificationConfiguration"},
		{"accelerate", 200, "AccelerateConfiguration"},
		{"requestPayment", 200, "RequestPaymentConfiguration"},
		{"encryption", 404, "ServerSideEncryptionConfigurationNotFoundError"},
		{"tagging", 404, "NoSuchTagSet"},
		{"cors", 404, "NoSuchCORSConfiguration"},
		{"website", 404, "NoSuchWebsiteConfiguration"},
		{"replication", 404, "ReplicationConfigurationNotFoundError"},
		{"ownershipControls", 404, "OwnershipControlsNotFoundError"},
		{"publicAccessBlock", 404, "NoSuchPublicAccessBlockConfiguration"},
		{"object-lock", 404, "ObjectLockConfigurationNotFoundError"},
		{"lifecycle", 404, "NoSuchLifecycleConfiguration"},
		{"policy", 404, "NoSuchBucketPolicy"},
		{"foo", 501, "NotImplemented"},
		{"analytics", 501, "NotImplemented"},
		{"intelligent-tiering", 501, "NotImplemented"},
		{"prefix=a&foo=1", 501, "NotImplemented"},
		{"list-type=2&Foo", 501, "NotImplemented"},
	}
	for _, base := range []string{b, b + "/"} {
		for _, c := range gets {
			target := base + "?" + c.query
			w := f.do(t, f.adminKey, http.MethodGet, target, nil)
			notListing("GET "+target, w)
			if w.Code != c.status {
				t.Errorf("GET %s: %d, want %d (%s)", target, w.Code, c.status, w.Body.String())
				continue
			}
			if c.status == 200 {
				if got := rootElem(w); got != c.code {
					t.Errorf("GET %s: root <%s>, want <%s>", target, got, c.code)
				}
			} else if got := itS3ErrCode(t, w); got != c.code {
				t.Errorf("GET %s: code %s, want %s", target, got, c.code)
			}
		}
	}

	// Specific bodies.
	w := f.do(t, f.adminKey, http.MethodGet, b+"?acl", nil)
	if body := w.Body.String(); !strings.Contains(body, "FULL_CONTROL") || !strings.Contains(body, f.admin.Username) ||
		strings.Contains(body, "AllUsers") || !strings.Contains(body, `xsi:type="CanonicalUser"`) {
		t.Errorf("private ?acl: %s", body)
	}
	if body := f.do(t, f.adminKey, http.MethodGet, "/"+f.pub.Name+"?acl", nil).Body.String(); !strings.Contains(body, "AllUsers") || !strings.Contains(body, "<Permission>READ</Permission>") {
		t.Errorf("public ?acl: %s", body)
	}
	if body := f.do(t, f.adminKey, http.MethodGet, b+"?policyStatus", nil).Body.String(); !strings.Contains(body, "<IsPublic>false</IsPublic>") {
		t.Errorf("private ?policyStatus: %s", body)
	}
	if body := f.do(t, f.adminKey, http.MethodGet, "/"+f.pub.Name+"?policyStatus", nil).Body.String(); !strings.Contains(body, "<IsPublic>true</IsPublic>") {
		t.Errorf("public ?policyStatus: %s", body)
	}
	if body := f.do(t, f.adminKey, http.MethodGet, b+"?requestPayment", nil).Body.String(); !strings.Contains(body, "<Payer>BucketOwner</Payer>") {
		t.Errorf("?requestPayment: %s", body)
	}

	// Listing parameters still list (V1, V2 with every SDK parameter,
	// versions, uploads, x-id).
	for _, q := range []string{
		"", "list-type=2", "list-type=2&prefix=&delimiter=%2F&encoding-type=url&fetch-owner=true&max-keys=10&start-after=a",
		"prefix=p&marker=a&max-keys=5&delimiter=/", "list-type=2&continuation-token=&x-id=ListObjectsV2",
	} {
		w := f.do(t, f.adminKey, http.MethodGet, b+"?"+q, nil)
		if w.Code != http.StatusOK || rootElem(w) != "ListBucketResult" {
			t.Errorf("GET ?%s: %d %s, want a listing", q, w.Code, w.Body.String())
		}
	}
	if w := f.do(t, f.adminKey, http.MethodGet, b+"?list-type=2", nil); !strings.Contains(w.Body.String(), "public.txt") {
		t.Errorf("listing lost objects: %s", w.Body.String())
	}
	for q, root := range map[string]string{
		"versions": "ListVersionsResult",
		"versions&prefix=&key-marker=&max-keys=10": "ListVersionsResult",
		"uploads": "ListMultipartUploadsResult",
		"uploads&prefix=a&max-uploads=5&key-marker=a":    "ListMultipartUploadsResult",
		"uploads&upload-id-marker=x&delimiter=%2F&x-id=": "ListMultipartUploadsResult",
	} {
		w := f.do(t, f.adminKey, http.MethodGet, b+"?"+q, nil)
		if w.Code != http.StatusOK || rootElem(w) != root {
			t.Errorf("GET ?%s: %d %s, want <%s>", q, w.Code, w.Body.String(), root)
		}
	}

	// Access: a caller without list access learns nothing about the bucket's
	// configuration; unknown sub-resources are 501 without touching it.
	for _, q := range []string{"acl", "cors", "policyStatus", "location", "encryption"} {
		expectS3Err(t, "nobody GET ?"+q, f.do(t, f.nobodyKey, http.MethodGet, b+"?"+q, nil), http.StatusForbidden, "AccessDenied")
	}
	expectS3Err(t, "nobody GET ?foo", f.do(t, f.nobodyKey, http.MethodGet, b+"?foo", nil), http.StatusNotImplemented, "NotImplemented")
	expectS3Err(t, "GET ?acl unknown bucket", f.do(t, f.adminKey, http.MethodGet, "/"+itName("nob")+"?acl", nil), http.StatusNotFound, "NoSuchBucket")
	// A user with list access sees the stubs.
	if w := f.do(t, f.userKey, http.MethodGet, b+"?acl", nil); w.Code != http.StatusOK {
		t.Errorf("user GET ?acl: %d %s", w.Code, w.Body.String())
	}

	// PUT: implemented sub-resources still dispatch; the rest is 501;
	// plain PUT is still the create-bucket probe.
	for _, q := range []string{"acl", "cors", "tagging", "website", "encryption", "replication", "publicAccessBlock", "foo"} {
		w := f.do(t, f.adminKey, http.MethodPut, b+"?"+q, []byte("<x/>"))
		expectS3Err(t, "PUT ?"+q, w, http.StatusNotImplemented, "NotImplemented")
	}
	if w := f.do(t, f.adminKey, http.MethodPut, b+"?acl", nil); !strings.Contains(w.Body.String(), "Public read access") {
		t.Errorf("PUT ?acl message: %s", w.Body.String())
	}
	expectS3Err(t, "PUT ?versioning (malformed)", f.do(t, f.adminKey, http.MethodPut, b+"?versioning", []byte("nope")), http.StatusBadRequest, "MalformedXML")
	expectS3Err(t, "PUT bucket", f.do(t, f.adminKey, http.MethodPut, b, nil), http.StatusConflict, "BucketAlreadyOwnedByYou")
	expectS3Err(t, "PUT bucket/", f.do(t, f.adminKey, http.MethodPut, b+"/", nil), http.StatusConflict, "BucketAlreadyOwnedByYou")

	// DELETE: ?lifecycle now reaches its handler for /bucket (it used to be
	// redirected to /bucket/ and refused as a bucket deletion).
	for _, base := range []string{b, b + "/"} {
		if w := f.do(t, f.adminKey, http.MethodDelete, base+"?lifecycle", nil); w.Code != http.StatusNoContent {
			t.Errorf("DELETE %s?lifecycle: %d %s", base, w.Code, w.Body.String())
		}
		for _, q := range []string{"cors", "tagging", "website", "encryption", "foo"} {
			expectS3Err(t, "DELETE "+base+"?"+q, f.do(t, f.adminKey, http.MethodDelete, base+"?"+q, nil), http.StatusNotImplemented, "NotImplemented")
		}
		w := f.do(t, f.adminKey, http.MethodDelete, base, nil)
		expectS3Err(t, "DELETE "+base, w, http.StatusForbidden, "AccessDenied")
		if !strings.Contains(w.Body.String(), "Bucket deletion via S3 API is not supported") {
			t.Errorf("DELETE %s: %s", base, w.Body.String())
		}
	}
	var still models.Bucket
	if err := database.DB.First(&still, "id = ?", f.bucket.ID).Error; err != nil {
		t.Fatal("bucket deleted")
	}

	// POST: ?delete still works; anything else is 501.
	expectS3Err(t, "POST ?foo", f.do(t, f.adminKey, http.MethodPost, b+"?foo", nil), http.StatusNotImplemented, "NotImplemented")
	expectS3Err(t, "POST (no query)", f.do(t, f.adminKey, http.MethodPost, b, nil), http.StatusNotImplemented, "NotImplemented")
	del := []byte(`<Delete><Object><Key>nothing-here</Key></Object></Delete>`)
	if w := f.do(t, f.adminKey, http.MethodPost, b+"?delete", del); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "DeleteResult") {
		t.Errorf("POST ?delete: %d %s", w.Code, w.Body.String())
	}
}

func TestIntegrationS3BucketEncryptionWithSSE(t *testing.T) {
	cfg := itConfig(t)
	cfg.Storage.S3SSE = true
	admin := mkUser(t, itName("sseadm"), "password-123", true, false)
	s3b := itBucket(t, admin.ID, func(b *models.Bucket) { b.StorageBackend = "s3" })
	local := itBucket(t, admin.ID, nil)
	key := itAccessKey(t, admin, "sse-"+uuid.NewString()[:6], nil)
	r := SetupS3Router(cfg)
	w := s3Signed(t, r, key, http.MethodGet, "/"+s3b.Name+"?encryption", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<SSEAlgorithm>AES256</SSEAlgorithm>") {
		t.Errorf("S3-backed bucket with S3_SSE: %d %s", w.Code, w.Body.String())
	}
	expectS3Err(t, "local bucket", s3Signed(t, r, key, http.MethodGet, "/"+local.Name+"?encryption", nil), http.StatusNotFound, "ServerSideEncryptionConfigurationNotFoundError")
}

// Unsigned requests for any bucket sub-resource are rejected exactly as
// before, public bucket or not.
func TestIntegrationS3BucketSubresourcesStaySigned(t *testing.T) {
	f := newBPFixture(t)
	for _, bucket := range []string{f.bucket.Name, f.pub.Name} {
		b := "/" + bucket
		for _, c := range []struct{ method, target string }{
			{http.MethodGet, b + "?policy"}, {http.MethodGet, b + "/?policy"}, {http.MethodGet, b + "?policyStatus"},
			{http.MethodGet, b + "?acl"}, {http.MethodGet, b + "?cors"}, {http.MethodGet, b + "?foo"},
			{http.MethodPut, b + "?policy"}, {http.MethodPut, b + "?acl"}, {http.MethodPut, b + "?foo"},
			{http.MethodDelete, b + "?policy"}, {http.MethodDelete, b}, {http.MethodDelete, b + "?foo"},
			{http.MethodPost, b + "?foo"},
		} {
			req := httptest.NewRequest(c.method, c.target, strings.NewReader(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:*","Resource":"*"}]}`))
			w := httptest.NewRecorder()
			f.s3.ServeHTTP(w, req)
			assertUnsignedRejected(t, c.method+" "+c.target, w)
		}
	}
	var n int64
	database.DB.Model(&models.BucketPolicy{}).Where("bucket_id IN (?, ?)", f.bucket.ID, f.pub.ID).Count(&n)
	if n != 0 {
		t.Errorf("anonymous requests stored %d bucket policies", n)
	}
}

// REST: DELETE /api/buckets/:name/policy is admin only and audit-logged; PUT
// is audit-logged; GET follows admin-or-s3:GetBucketPolicy.
func TestIntegrationRESTBucketPolicyDeleteAndAudit(t *testing.T) {
	f := newBPFixture(t)
	h := NewBucketHandler(f.cfg)
	rest := func(u models.User) *gin.Engine {
		r := itRouter(u)
		r.PUT("/api/buckets/:name/policy", middleware.AdminMiddleware(), h.SetBucketPolicy)
		r.GET("/api/buckets/:name/policy", h.GetBucketPolicy)
		r.DELETE("/api/buckets/:name/policy", middleware.AdminMiddleware(), h.DeleteBucketPolicy)
		return r
	}
	path := "/api/buckets/" + f.bucket.Name + "/policy"
	doc := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":{"AWS":"*"},"Action":"s3:GetObject","Resource":"arn:aws:s3:::%s/secret/*"}]}`, f.bucket.Name)
	body, _ := json.Marshal(map[string]string{"policy": doc})

	if w := itDo(rest(f.user), http.MethodPut, path, body); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin REST PUT: %d", w.Code)
	}
	if w := itDo(rest(f.admin), http.MethodPut, path, body); w.Code != http.StatusOK {
		t.Fatalf("admin REST PUT: %d %s", w.Code, w.Body.String())
	}
	if n := auditCount(t, "bucket.policy.set", f.bucket.ID.String()); n != 1 {
		t.Errorf("REST PUT audit entries: %d", n)
	}
	// Same document through the S3 API.
	if w := f.do(t, f.adminKey, http.MethodGet, "/"+f.bucket.Name+"?policy", nil); !sameJSON(w.Body.String(), doc) {
		t.Errorf("S3 GET after REST PUT: %s", w.Body.String())
	}
	// Invalid documents get the validator's message.
	bad, _ := json.Marshal(map[string]string{"policy": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"x"},"Action":"s3:GetObject","Resource":"*"}]}`})
	if w := itDo(rest(f.admin), http.MethodPut, path, bad); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "supported forms") {
		t.Errorf("REST PUT invalid: %d %s", w.Code, w.Body.String())
	}

	// GET: admin and the s3:GetBucketPolicy grantee; not a plain reader.
	for _, c := range []struct {
		u    models.User
		want int
	}{{f.admin, 200}, {f.rdr, 200}, {f.user, 403}} {
		w := itDo(rest(c.u), http.MethodGet, path, nil)
		if w.Code != c.want {
			t.Errorf("REST GET as %s: %d, want %d", c.u.Username, w.Code, c.want)
		}
		if c.want == 200 {
			var got map[string]string
			_ = json.Unmarshal(w.Body.Bytes(), &got)
			if !sameJSON(got["policy"], doc) {
				t.Errorf("REST GET as %s: %s", c.u.Username, w.Body.String())
			}
		}
	}

	// DELETE: non-admins refused (policy kept), admin deletes, idempotent.
	for _, u := range []models.User{f.user, f.rdr} {
		if w := itDo(rest(u), http.MethodDelete, path, nil); w.Code != http.StatusForbidden {
			t.Errorf("REST DELETE as %s: %d, want 403", u.Username, w.Code)
		}
	}
	if w := itDo(rest(f.admin), http.MethodGet, path, nil); w.Code != http.StatusOK {
		t.Fatalf("policy removed by a refused delete: %d", w.Code)
	}
	for i := 0; i < 2; i++ {
		if w := itDo(rest(f.admin), http.MethodDelete, path, nil); w.Code != http.StatusOK {
			t.Fatalf("admin REST DELETE #%d: %d %s", i+1, w.Code, w.Body.String())
		}
	}
	if w := itDo(rest(f.admin), http.MethodGet, path, nil); w.Code != http.StatusNotFound {
		t.Errorf("REST GET after delete: %d", w.Code)
	}
	if n := auditCount(t, "bucket.policy.delete", f.bucket.ID.String()); n != 1 {
		t.Errorf("REST DELETE audit entries: %d, want 1", n)
	}
	var entry models.AuditLog
	database.DB.Where("action = ? AND resource_id = ?", "bucket.policy.delete", f.bucket.ID.String()).First(&entry)
	var meta map[string]interface{}
	_ = json.Unmarshal([]byte(entry.Metadata), &meta)
	if entry.UserID != f.admin.ID || meta["via"] != "console" || !sameJSON(fmt.Sprint(meta["previous_policy"]), doc) {
		t.Errorf("REST delete audit: user=%s meta=%v", entry.UserID, meta)
	}
	if w := itDo(rest(f.admin), http.MethodDelete, "/api/buckets/"+itName("nob")+"/policy", nil); w.Code != http.StatusNotFound {
		t.Errorf("REST DELETE unknown bucket: %d", w.Code)
	}
}
