package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// Public-read buckets: unsigned GET/HEAD of an object's current version is
// allowed on buckets with is_public; everything else on the S3 listener still
// requires a signature. These tests drive the real S3 router (auth
// middleware + routes), so they cover how the anonymous decision is made and
// that no other route is reachable without credentials.

// pubFixture is a public and a private bucket with a few objects, plus the
// real S3 router.
type pubFixture struct {
	cfg     *config.Config
	admin   models.User
	pub     models.Bucket
	priv    models.Bucket
	s3      *gin.Engine
	content string
}

func newPubFixture(t *testing.T, publicReadRateLimit int) *pubFixture {
	t.Helper()
	cfg := itConfig(t)
	cfg.Auth.PublicReadRateLimit = publicReadRateLimit
	f := &pubFixture{cfg: cfg, content: "hello public world"}
	f.admin = mkUser(t, itName("padm"), "password-123", true, false)
	f.pub = itBucket(t, f.admin.ID, func(b *models.Bucket) { b.IsPublic = true })
	f.priv = itBucket(t, f.admin.ID, nil)

	up := itS3Router(cfg, f.admin)
	s3PutString(t, up, f.pub.Name, "hello.txt", f.content)
	s3PutString(t, up, f.pub.Name, "dir/nested%20file.txt", "nested") // key "dir/nested file.txt"
	s3PutString(t, up, f.pub.Name, "secret/x", "top secret")
	s3PutString(t, up, f.priv.Name, "hello.txt", "private")
	// An uploader-controlled active document.
	req := httptest.NewRequest(http.MethodPut, "/"+f.pub.Name+"/page.html", strings.NewReader("<script>alert(1)</script>"))
	req.Header.Set("Content-Type", "text/html")
	w := httptest.NewRecorder()
	up.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT page.html: %d %s", w.Code, w.Body.String())
	}

	f.s3 = SetupS3Router(cfg)
	return f
}

// anon sends an unsigned request to the real S3 router.
func (f *pubFixture) anon(method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.s3.ServeHTTP(w, req)
	return w
}

// signed sends a SigV4 header-signed request as the owner of key.
func (f *pubFixture) signed(t *testing.T, key models.AccessKey, secret, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
	creds := aws.Credentials{AccessKeyID: key.AccessKey, SecretAccessKey: secret}
	if err := v4.NewSigner().SignHTTP(context.Background(), creds, req, "UNSIGNED-PAYLOAD", "s3", "us-east-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	f.s3.ServeHTTP(w, req)
	return w
}

// unsignedRejection is what the S3 listener has always answered to an unsigned
// request that is not a public read (unchanged by public-read support).
func assertUnsignedRejected(t *testing.T, what string, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "Missing authorization header") {
		t.Errorf("%s: %d %q, want the unchanged unsigned rejection (401 AccessDenied, Missing authorization header)", what, w.Code, w.Body.String())
	}
}

func TestIntegrationPublicReadAnonymousObjectReads(t *testing.T) {
	f := newPubFixture(t, 0)
	base := "/" + f.pub.Name

	// GET: content, current-version object read.
	w := f.anon(http.MethodGet, base+"/hello.txt", nil)
	if w.Code != http.StatusOK || w.Body.String() != f.content {
		t.Fatalf("anonymous GET: %d %q", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("anonymous GET: missing nosniff")
	}
	// Keys with spaces/nesting, percent-encoded as a browser would.
	if w := f.anon(http.MethodGet, base+"/dir/nested%20file.txt", nil); w.Code != http.StatusOK || w.Body.String() != "nested" {
		t.Errorf("anonymous GET nested key: %d %q", w.Code, w.Body.String())
	}

	// HEAD.
	w = f.anon(http.MethodHead, base+"/hello.txt", nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Length") != fmt.Sprint(len(f.content)) {
		t.Errorf("anonymous HEAD: %d len=%q", w.Code, w.Header().Get("Content-Length"))
	}

	// Range.
	w = f.anon(http.MethodGet, base+"/hello.txt", map[string]string{"Range": "bytes=0-4"})
	if w.Code != http.StatusPartialContent || w.Body.String() != "hello" ||
		w.Header().Get("Content-Range") != fmt.Sprintf("bytes 0-4/%d", len(f.content)) {
		t.Errorf("anonymous Range GET: %d %q %q", w.Code, w.Body.String(), w.Header().Get("Content-Range"))
	}

	// response-* overrides are honored.
	w = f.anon(http.MethodGet, base+"/hello.txt?response-content-disposition=attachment%3B%20filename%3D%22greeting.txt%22&response-cache-control=no-store", nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Disposition") != `attachment; filename="greeting.txt"` ||
		w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("response-* overrides: %d disposition=%q cache=%q", w.Code, w.Header().Get("Content-Disposition"), w.Header().Get("Cache-Control"))
	}

	// Active content is served as an attachment, and an anonymous request
	// cannot opt out (inline) or turn a passive object into HTML.
	for _, target := range []string{
		base + "/page.html",
		base + "/page.html?response-content-disposition=inline",
		base + "/hello.txt?response-content-type=text%2Fhtml&response-content-disposition=inline",
	} {
		w := f.anon(http.MethodGet, target, nil)
		if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") ||
			w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s: %d disposition=%q nosniff=%q, want attachment", target, w.Code,
				w.Header().Get("Content-Disposition"), w.Header().Get("X-Content-Type-Options"))
		}
	}

	// Missing key on a public bucket: an ordinary 404.
	if w := f.anon(http.MethodGet, base+"/nope.txt", nil); w.Code != http.StatusNotFound {
		t.Errorf("anonymous GET missing key: %d", w.Code)
	}
	// bkt's internal version keyspace stays unreachable.
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		if w := f.anon(m, base+"/.bkt-versions/hello.txt", nil); w.Code != http.StatusNotFound {
			t.Errorf("anonymous %s .bkt-versions: %d, want 404", m, w.Code)
		}
	}
	// HEAD of a "folder" is not emulated for anonymous callers (no prefix probing).
	if w := f.anon(http.MethodHead, base+"/dir/", nil); w.Code != http.StatusNotFound {
		t.Errorf("anonymous HEAD dir/: %d, want 404", w.Code)
	}
}

func TestIntegrationPublicReadEverythingElseStaysSigned(t *testing.T) {
	f := newPubFixture(t, 0)
	base := "/" + f.pub.Name

	// Sub-resources / other versions / anything but response-* parameters.
	for _, q := range []string{
		"versionId=null", "acl", "tagging", "uploadId=abc", "attributes", "legal-hold", "retention",
		"torrent", "partNumber=1", "response-content-type=text/plain&acl",
		"X-Amz-Signature=abc", "Signature=abc&AWSAccessKeyId=x", "response-content-type=a;b",
	} {
		assertUnsignedRejected(t, "GET ?"+q, f.anon(http.MethodGet, base+"/hello.txt?"+q, nil))
		assertUnsignedRejected(t, "HEAD ?"+q, f.anon(http.MethodHead, base+"/hello.txt?"+q, nil))
	}

	// Listing and bucket-level requests.
	for _, target := range []string{"/", base, base + "/", base + "?list-type=2", base + "?versions", base + "/?uploads"} {
		assertUnsignedRejected(t, "GET "+target, f.anon(http.MethodGet, target, nil))
	}
	assertUnsignedRejected(t, "HEAD bucket", f.anon(http.MethodHead, base, nil))
	assertUnsignedRejected(t, "HEAD bucket/", f.anon(http.MethodHead, base+"/", nil))

	// Writes, deletes, multipart.
	for _, c := range []struct{ method, target string }{
		{http.MethodPut, base + "/hello.txt"},
		{http.MethodPut, base + "/new.txt"},
		{http.MethodPut, base},
		{http.MethodPost, base + "/hello.txt?uploads"},
		{http.MethodPost, base + "?delete"},
		{http.MethodDelete, base + "/hello.txt"},
		{http.MethodDelete, base + "/"},
	} {
		assertUnsignedRejected(t, c.method+" "+c.target, f.anon(c.method, c.target, nil))
	}
	if code, body := s3GetString(t, itS3Router(f.cfg, f.admin), f.pub.Name, "hello.txt"); code != http.StatusOK || body != f.content {
		t.Errorf("object changed by anonymous writes: %d %q", code, body)
	}

	// Private buckets and unknown buckets: unchanged, and indistinguishable.
	priv := f.anon(http.MethodGet, "/"+f.priv.Name+"/hello.txt", nil)
	assertUnsignedRejected(t, "private GET", priv)
	missing := f.anon(http.MethodGet, "/"+itName("nobucket")+"/hello.txt", nil)
	assertUnsignedRejected(t, "unknown bucket GET", missing)
	if priv.Body.String() != missing.Body.String() {
		t.Errorf("private vs unknown bucket responses differ: %q vs %q", priv.Body.String(), missing.Body.String())
	}
	assertUnsignedRejected(t, "private HEAD", f.anon(http.MethodHead, "/"+f.priv.Name+"/hello.txt", nil))
}

func TestIntegrationPublicReadBucketPolicyDeny(t *testing.T) {
	f := newPubFixture(t, 0)
	doc := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Deny","Principal":"*","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%[1]s/secret/*"]},
		{"Effect":"Deny","Principal":["someone"],"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%[1]s/hello.txt"]}]}`, f.pub.Name)
	if err := database.DB.Create(&models.BucketPolicy{BucketID: f.pub.ID, PolicyDocument: doc}).Error; err != nil {
		t.Fatal(err)
	}
	base := "/" + f.pub.Name

	w := f.anon(http.MethodGet, base+"/secret/x", nil)
	if w.Code != http.StatusForbidden || itS3ErrCode(t, w) != "AccessDenied" || strings.Contains(w.Body.String(), "top secret") {
		t.Errorf("anonymous GET denied key: %d %s", w.Code, w.Body.String())
	}
	if w := f.anon(http.MethodHead, base+"/secret/x", nil); w.Code != http.StatusForbidden {
		t.Errorf("anonymous HEAD denied key: %d", w.Code)
	}
	// A Deny scoped to a named user does not apply to anonymous readers.
	if w := f.anon(http.MethodGet, base+"/hello.txt", nil); w.Code != http.StatusOK || w.Body.String() != f.content {
		t.Errorf("anonymous GET other key: %d %q", w.Code, w.Body.String())
	}

	// An unparseable stored bucket policy fails closed for anonymous reads.
	database.DB.Model(&models.BucketPolicy{}).Where("bucket_id = ?", f.pub.ID).Update("policy_document", `{"Version":"2012-10-17","Statement":[]}`)
	if w := f.anon(http.MethodGet, base+"/hello.txt", nil); w.Code != http.StatusForbidden {
		t.Errorf("anonymous GET with a broken bucket policy: %d, want 403", w.Code)
	}
}

func TestIntegrationPublicReadSignedRequestsUnchanged(t *testing.T) {
	f := newPubFixture(t, 0)
	user := mkUser(t, itName("pusr"), "password-123", false, false)
	key := itAccessKey(t, user, "pub-"+uuid.NewString()[:6], nil)
	secret := "secret-" + key.Name
	base := "/" + f.pub.Name

	// A signed caller is judged by policies alone; a user without any grant is
	// still denied on a public bucket (listing and reads alike).
	for _, c := range []struct{ method, target string }{
		{http.MethodGet, base},
		{http.MethodGet, base + "/"},
		{http.MethodGet, base + "/hello.txt"},
		{http.MethodPut, base + "/new.txt"},
		{http.MethodDelete, base + "/hello.txt"},
	} {
		w := f.signed(t, key, secret, c.method, c.target)
		if w.Code != http.StatusForbidden {
			t.Errorf("signed %s %s without policy: %d %s, want 403", c.method, c.target, w.Code, w.Body.String())
		}
	}
	// ... and the admin's signed requests work as before.
	adminKey := itAccessKey(t, f.admin, "padm-"+uuid.NewString()[:6], nil)
	if w := f.signed(t, adminKey, "secret-"+adminKey.Name, http.MethodGet, base+"?list-type=2"); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "hello.txt") {
		t.Errorf("signed admin listing: %d %s", w.Code, w.Body.String())
	}
	if w := f.signed(t, adminKey, "secret-"+adminKey.Name, http.MethodGet, base+"/hello.txt"); w.Code != http.StatusOK || w.Body.String() != f.content {
		t.Errorf("signed admin GET: %d %q", w.Code, w.Body.String())
	}
	// A bad signature is not downgraded to an anonymous read.
	req := httptest.NewRequest(http.MethodGet, base+"/hello.txt", nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=NOPE/20240101/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=00")
	w := httptest.NewRecorder()
	f.s3.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "InvalidAccessKeyId") {
		t.Errorf("bad credentials on a public bucket: %d %s", w.Code, w.Body.String())
	}
}

func TestIntegrationPublicReadRateLimit(t *testing.T) {
	f := newPubFixture(t, 3)
	for i := 0; i < 3; i++ {
		if w := f.anon(http.MethodGet, "/"+f.pub.Name+"/hello.txt", nil); w.Code != http.StatusOK {
			t.Fatalf("request %d: %d", i+1, w.Code)
		}
	}
	w := f.anon(http.MethodGet, "/"+f.pub.Name+"/hello.txt", nil)
	if w.Code != http.StatusServiceUnavailable || itS3ErrCode(t, w) != "SlowDown" || w.Header().Get("Retry-After") == "" {
		t.Fatalf("over the limit: %d %s (Retry-After %q), want 503 SlowDown", w.Code, w.Body.String(), w.Header().Get("Retry-After"))
	}
	// Another client has its own budget.
	req := httptest.NewRequest(http.MethodGet, "/"+f.pub.Name+"/hello.txt", nil)
	req.RemoteAddr = "198.51.100.7:4000"
	w = httptest.NewRecorder()
	f.s3.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("other client: %d", w.Code)
	}
	// Signed traffic is not subject to the public-read limiter.
	adminKey := itAccessKey(t, f.admin, "rl-"+uuid.NewString()[:6], nil)
	if w := f.signed(t, adminKey, "secret-"+adminKey.Name, http.MethodGet, "/"+f.pub.Name+"/hello.txt"); w.Code != http.StatusOK {
		t.Errorf("signed request after anonymous limit: %d", w.Code)
	}
}

func TestIntegrationPublicReadSettingsToggle(t *testing.T) {
	f := newPubFixture(t, 0)
	cfg := f.cfg
	h := NewBucketHandler(cfg)
	settings := func(u models.User, body string) *httptest.ResponseRecorder {
		r := itRouter(u)
		r.PUT("/api/buckets/:name/settings", h.SetBucketSettings)
		req := httptest.NewRequest(http.MethodPut, "/api/buckets/"+f.pub.Name+"/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	isPublic := func() bool {
		var b models.Bucket
		database.DB.First(&b, "id = ?", f.pub.ID)
		return b.IsPublic
	}

	// A non-admin holding every bucket-configuration action cannot flip it.
	cfgUser := mkUser(t, itName("pcfg"), "password-123", false, false)
	p := models.Policy{Name: itName("pol"), Document: fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*"],"Resource":["arn:aws:s3:::%[1]s","arn:aws:s3:::%[1]s/*"]}]}`, f.pub.Name)}
	if err := database.DB.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.DB.Model(&cfgUser).Association("Policies").Append(&p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.DB.Exec(`DELETE FROM user_policies WHERE policy_id = ?`, p.ID)
		database.DB.Exec(`DELETE FROM policies WHERE id = ?`, p.ID)
	})
	if w := settings(cfgUser, `{"is_public":false}`); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin is_public: %d %s, want 403", w.Code, w.Body.String())
	}
	// Combined with a field they may change: still rejected as a whole.
	if w := settings(cfgUser, `{"is_public":false,"quota_bytes":1000}`); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin is_public+quota: %d, want 403", w.Code)
	}
	if !isPublic() {
		t.Fatal("bucket stopped being public after rejected requests")
	}
	// Their other settings still work.
	if w := settings(cfgUser, `{"quota_bytes":0}`); w.Code != http.StatusOK {
		t.Fatalf("non-admin quota: %d %s", w.Code, w.Body.String())
	}

	// The admin can turn it off; anonymous reads stop immediately.
	if w := settings(f.admin, `{"is_public":false}`); w.Code != http.StatusOK {
		t.Fatalf("admin is_public=false: %d %s", w.Code, w.Body.String())
	}
	if isPublic() {
		t.Fatal("is_public still set")
	}
	assertUnsignedRejected(t, "GET after disabling", f.anon(http.MethodGet, "/"+f.pub.Name+"/hello.txt", nil))

	var entry models.AuditLog
	if err := database.DB.Where("action = ? AND resource_id = ?", "bucket.public_access", f.pub.ID.String()).
		Order("created_at DESC").First(&entry).Error; err != nil {
		t.Fatalf("no bucket.public_access audit entry: %v", err)
	}
	var meta map[string]interface{}
	_ = json.Unmarshal([]byte(entry.Metadata), &meta)
	if entry.UserID != f.admin.ID || meta["old"] != true || meta["new"] != false {
		t.Errorf("audit entry: user=%s meta=%v", entry.UserID, meta)
	}
	t.Cleanup(func() { database.DB.Exec(`DELETE FROM audit_logs WHERE resource_id = ?`, f.pub.ID.String()) })

	// ... and back on.
	if w := settings(f.admin, `{"is_public":true}`); w.Code != http.StatusOK {
		t.Fatalf("admin is_public=true: %d %s", w.Code, w.Body.String())
	}
	if w := f.anon(http.MethodGet, "/"+f.pub.Name+"/hello.txt", nil); w.Code != http.StatusOK {
		t.Errorf("GET after re-enabling: %d", w.Code)
	}

	// GET /api/buckets/:name advertises the public URL base (admin and
	// non-admin views), and omits it for private buckets.
	get := func(u models.User, name string) map[string]interface{} {
		r := itRouter(u)
		r.GET("/api/buckets/:name", h.GetBucket)
		w := itDo(r, http.MethodGet, "/api/buckets/"+name, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("GET bucket %s as %s: %d %s", name, u.Username, w.Code, w.Body.String())
		}
		var m map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return m
	}
	for _, u := range []models.User{f.admin, cfgUser} {
		m := get(u, f.pub.Name)
		base, _ := m["public_url_base"].(string)
		if !strings.HasSuffix(base, "/"+f.pub.Name) || m["is_public"] != true || m["name"] != f.pub.Name {
			t.Errorf("%s: public_url_base=%q is_public=%v name=%v", u.Username, base, m["is_public"], m["name"])
		}
	}
	if m := get(f.admin, f.priv.Name); m["public_url_base"] != nil {
		t.Errorf("private bucket has public_url_base %v", m["public_url_base"])
	}
}

// Defense in depth: even if the auth middleware admitted an anonymous request
// it should not have, every S3 handler other than the object GET/HEAD fails
// closed (403, no panic), and GET/HEAD refuse sub-resources and versions.
func TestIntegrationPublicReadHandlersFailClosedWithoutCaller(t *testing.T) {
	f := newPubFixture(t, 0)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(func(c *gin.Context) { c.Set(middleware.CtxS3Anonymous, true); c.Next() })
	registerS3Routes(r, NewS3APIHandler(f.cfg))

	b := "/" + f.pub.Name
	cases := []struct {
		method, target string
		hdr            map[string]string
	}{
		{http.MethodGet, "/", nil},
		{http.MethodHead, b, nil},
		{http.MethodHead, b + "/", nil},
		{http.MethodGet, b, nil},
		{http.MethodGet, b + "/", nil},
		{http.MethodGet, b + "?versions", nil},
		{http.MethodGet, b + "?lifecycle", nil},
		{http.MethodGet, b + "?uploads", nil},
		{http.MethodPost, b + "?delete", nil},
		{http.MethodPut, b, nil},
		{http.MethodPut, b + "?versioning", nil},
		{http.MethodPut, b + "?lifecycle", nil},
		{http.MethodPut, b + "/", nil},
		{http.MethodPut, b + "/hello.txt", nil},
		{http.MethodPut, b + "/hello.txt?tagging", nil},
		{http.MethodPut, b + "/hello.txt?uploadId=x&partNumber=1", nil},
		{http.MethodPut, b + "/copy.txt", map[string]string{"X-Amz-Copy-Source": b + "/hello.txt"}},
		{http.MethodPost, b + "/hello.txt?uploads", nil},
		{http.MethodPost, b + "/hello.txt?uploadId=x", nil},
		{http.MethodDelete, b + "/", nil},
		{http.MethodDelete, b + "/hello.txt", nil},
		{http.MethodDelete, b + "/hello.txt?uploadId=x", nil},
		{http.MethodDelete, b + "/hello.txt?tagging", nil},
		{http.MethodGet, b + "/hello.txt?uploadId=x", nil},
		{http.MethodGet, b + "/hello.txt?tagging", nil},
		{http.MethodGet, b + "/hello.txt?versionId=null", nil},
		{http.MethodHead, b + "/hello.txt?versionId=null", nil},
		{http.MethodGet, b + "/hello.txt?acl", nil},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.target, strings.NewReader("x"))
		for k, v := range c.hdr {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("anonymous %s %s reached a handler: %d %s, want 403", c.method, c.target, w.Code, w.Body.String())
		}
	}
	if code, body := s3GetString(t, itS3Router(f.cfg, f.admin), f.pub.Name, "hello.txt"); code != http.StatusOK || body != f.content {
		t.Errorf("object changed: %d %q", code, body)
	}
	if _, ok := currentRow(t, f.pub.ID, "copy.txt"); ok {
		t.Error("anonymous copy created an object")
	}
	// The object read itself still works in this setup.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, b+"/hello.txt", nil))
	if w.Code != http.StatusOK || w.Body.String() != f.content {
		t.Errorf("anonymous GET: %d %q", w.Code, w.Body.String())
	}
}
