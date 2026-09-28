package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"bkt/internal/database"
	"bkt/internal/models"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Public-read for SIGNED callers (AWS semantics): an object read on a public
// bucket is allowed for every authenticated user unless a policy (user,
// group, or bucket policy naming the user / "*" / no Principal) explicitly
// denies it. Listing, versions, tagging, writes and deletes still need
// policies. Postgres-gated; see integration_pg_test.go.

// signedPubCaller is a non-admin user with an access key, plus the routes a
// console session of that user reaches.
type signedPubCaller struct {
	f       *pubFixture
	user    models.User
	key     models.AccessKey
	secret  string
	console *gin.Engine
}

func newSignedPubCaller(t *testing.T, f *pubFixture, prefix string) *signedPubCaller {
	t.Helper()
	u := mkUser(t, itName(prefix), "password-123", false, false)
	k := itAccessKey(t, u, prefix+"-"+uuid.NewString()[:6], nil)
	r := itConsoleRouter(f.cfg, u)
	h := NewBucketHandler(f.cfg)
	r.GET("/api/buckets", h.ListBuckets)
	r.POST("/api/buckets/:name/objects/presign", h.PresignObject)
	r.POST("/api/buckets/:name/objects/move", h.MoveObject)
	return &signedPubCaller{f: f, user: u, key: k, secret: "secret-" + k.Name, console: r}
}

// s3 sends a SigV4 header-signed S3 request with optional extra headers.
func (c *signedPubCaller) s3(t *testing.T, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	req.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
	creds := aws.Credentials{AccessKeyID: c.key.AccessKey, SecretAccessKey: c.secret}
	if err := v4.NewSigner().SignHTTP(context.Background(), creds, req, "UNSIGNED-PAYLOAD", "s3", "us-east-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c.f.s3.ServeHTTP(w, req)
	return w
}

// presign asks the console for a presigned GET URL; on success it also
// fetches the URL from the S3 router and returns that response.
func (c *signedPubCaller) presign(t *testing.T, bucket, key string) (int, *httptest.ResponseRecorder) {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{"key": key, "expires_in": 600})
	w := itDo(c.console, http.MethodPost, "/api/buckets/"+bucket+"/objects/presign", body)
	if w.Code != http.StatusOK {
		return w.Code, nil
	}
	var resp struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(resp.URL)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, u.RequestURI(), nil)
	req.Host = u.Host
	got := httptest.NewRecorder()
	c.f.s3.ServeHTTP(got, req)
	return w.Code, got
}

func (c *signedPubCaller) bucketNames(t *testing.T) map[string]bool {
	t.Helper()
	w := itDo(c.console, http.MethodGet, "/api/buckets", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list buckets: %d %s", w.Code, w.Body.String())
	}
	var list []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("list buckets: %v %s", err, w.Body.String())
	}
	names := map[string]bool{}
	for _, b := range list {
		names[b.Name] = true
	}
	return names
}

// itPolicy creates a policy and removes it (and its attachments) at the end.
func itPolicy(t *testing.T, doc string) models.Policy {
	t.Helper()
	p := models.Policy{ID: uuid.New(), Name: itName("pol"), Document: doc}
	if err := database.DB.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.DB.Exec(`DELETE FROM user_policies WHERE policy_id = ?`, p.ID)
		database.DB.Exec(`DELETE FROM group_policies WHERE policy_id = ?`, p.ID)
		database.DB.Exec(`DELETE FROM policies WHERE id = ?`, p.ID)
	})
	return p
}

func attachUserPolicy(t *testing.T, u models.User, p models.Policy) {
	t.Helper()
	if err := database.DB.Exec(`INSERT INTO user_policies (user_id, policy_id) VALUES (?, ?)`, u.ID, p.ID).Error; err != nil {
		t.Fatal(err)
	}
}

// attachGroupPolicy puts u in a fresh group carrying p.
func attachGroupPolicy(t *testing.T, u models.User, p models.Policy) {
	t.Helper()
	gid := uuid.New()
	if err := database.DB.Exec(`INSERT INTO groups (id, name, created_at, updated_at) VALUES (?, ?, now(), now())`, gid, itName("grp")).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.DB.Exec(`DELETE FROM user_groups WHERE group_id = ?`, gid)
		database.DB.Exec(`DELETE FROM group_policies WHERE group_id = ?`, gid)
		database.DB.Exec(`DELETE FROM groups WHERE id = ?`, gid)
	})
	database.DB.Exec(`INSERT INTO group_policies (group_id, policy_id) VALUES (?, ?)`, gid, p.ID)
	database.DB.Exec(`INSERT INTO user_groups (group_id, user_id) VALUES (?, ?)`, gid, u.ID)
}

func TestIntegrationPublicReadSignedUserWithoutPolicy(t *testing.T) {
	f := newPubFixture(t, 0)
	f.cfg.Server.S3PublicEndpoint = "http://s3.bkt.test"
	c := newSignedPubCaller(t, f, "psu")
	pub := "/" + f.pub.Name

	// Object reads: allowed on every path.
	if w := c.s3(t, http.MethodGet, pub+"/hello.txt", nil); w.Code != http.StatusOK || w.Body.String() != f.content {
		t.Errorf("S3 GET: %d %q", w.Code, w.Body.String())
	}
	if w := c.s3(t, http.MethodHead, pub+"/hello.txt", nil); w.Code != http.StatusOK {
		t.Errorf("S3 HEAD: %d", w.Code)
	}
	if w := itDo(c.console, http.MethodGet, objectURL(f.pub.Name, "hello.txt"), nil); w.Code != http.StatusOK || w.Body.String() != f.content {
		t.Errorf("REST download: %d %q", w.Code, w.Body.String())
	}
	if w := itDo(c.console, http.MethodHead, objectURL(f.pub.Name, "hello.txt"), nil); w.Code != http.StatusOK {
		t.Errorf("REST head: %d", w.Code)
	}
	if code, got := c.presign(t, f.pub.Name, "hello.txt"); code != http.StatusOK || got == nil || got.Code != http.StatusOK || got.Body.String() != f.content {
		t.Errorf("presign: %d, URL fetch %+v", code, got)
	}

	// CopyObject from a public-read source works with PutObject on the
	// destination only.
	dst := itBucket(t, f.admin.ID, nil)
	attachUserPolicy(t, c.user, itPolicy(t, fmt.Sprintf(
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:PutObject"],"Resource":["arn:aws:s3:::%s/*"]}]}`, dst.Name)))
	if w := c.s3(t, http.MethodPut, "/"+dst.Name+"/copy.txt", map[string]string{"X-Amz-Copy-Source": pub + "/hello.txt"}); w.Code != http.StatusOK {
		t.Errorf("CopyObject from public source: %d %s", w.Code, w.Body.String())
	}
	if _, ok := currentRow(t, dst.ID, "copy.txt"); !ok {
		t.Error("CopyObject did not create the destination object")
	}

	// Everything else still needs a policy grant.
	for _, r := range []struct {
		what, method, target string
		hdr                  map[string]string
	}{
		{"S3 ListObjectsV2", http.MethodGet, pub + "?list-type=2", nil},
		{"S3 ListObjects", http.MethodGet, pub + "/", nil},
		{"S3 ListObjectVersions", http.MethodGet, pub + "?versions", nil},
		{"S3 PUT", http.MethodPut, pub + "/new.txt", nil},
		{"S3 DELETE", http.MethodDelete, pub + "/hello.txt", nil},
		{"S3 GET ?versionId", http.MethodGet, pub + "/hello.txt?versionId=null", nil},
		{"S3 HEAD ?versionId", http.MethodHead, pub + "/hello.txt?versionId=null", nil},
		{"S3 GET ?tagging", http.MethodGet, pub + "/hello.txt?tagging", nil},
		{"S3 copy within the public bucket", http.MethodPut, pub + "/copy.txt", map[string]string{"X-Amz-Copy-Source": pub + "/hello.txt"}},
	} {
		if w := c.s3(t, r.method, r.target, r.hdr); w.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s, want 403", r.what, w.Code, w.Body.String())
		}
	}
	for _, r := range []struct {
		what, method, target, body string
	}{
		{"REST object versions", http.MethodGet, "/api/buckets/" + f.pub.Name + "/object-versions?key=hello.txt", ""},
		{"REST delete", http.MethodDelete, objectURL(f.pub.Name, "hello.txt"), ""},
		{"REST move", http.MethodPost, "/api/buckets/" + f.pub.Name + "/objects/move", `{"source_key":"hello.txt","destination_key":"moved.txt"}`},
		{"REST folder move", http.MethodPost, "/api/buckets/" + f.pub.Name + "/folders/move", `{"source_prefix":"dir/","destination_prefix":"moved/"}`},
	} {
		var body []byte
		if r.body != "" {
			body = []byte(r.body)
		}
		if w := itDo(c.console, r.method, r.target, body); w.Code != http.StatusForbidden {
			t.Errorf("%s: %d %s, want 403", r.what, w.Code, w.Body.String())
		}
	}
	if code, body := s3GetString(t, itS3Router(f.cfg, f.admin), f.pub.Name, "hello.txt"); code != http.StatusOK || body != f.content {
		t.Errorf("object changed: %d %q", code, body)
	}

	// The console's bucket list does not show the public bucket to a user
	// without any grant on it (AWS lists only your own buckets).
	if names := c.bucketNames(t); names[f.pub.Name] {
		t.Error("public bucket listed for a user without access")
	}

	// A private bucket is unchanged: denied everywhere.
	priv := "/" + f.priv.Name + "/hello.txt"
	if w := c.s3(t, http.MethodGet, priv, nil); w.Code != http.StatusForbidden {
		t.Errorf("private S3 GET: %d", w.Code)
	}
	if w := c.s3(t, http.MethodHead, priv, nil); w.Code != http.StatusForbidden {
		t.Errorf("private S3 HEAD: %d", w.Code)
	}
	if w := itDo(c.console, http.MethodGet, objectURL(f.priv.Name, "hello.txt"), nil); w.Code != http.StatusForbidden {
		t.Errorf("private REST download: %d", w.Code)
	}
	if code, _ := c.presign(t, f.priv.Name, "hello.txt"); code != http.StatusForbidden {
		t.Errorf("private presign: %d", code)
	}
	if w := c.s3(t, http.MethodPut, "/"+dst.Name+"/copy2.txt", map[string]string{"X-Amz-Copy-Source": priv}); w.Code != http.StatusForbidden {
		t.Errorf("CopyObject from a private source: %d", w.Code)
	}

	// Turning public-read off takes effect immediately for signed callers too.
	database.DB.Model(&models.Bucket{}).Where("id = ?", f.pub.ID).Update("is_public", false)
	if w := c.s3(t, http.MethodGet, pub+"/hello.txt", nil); w.Code != http.StatusForbidden {
		t.Errorf("S3 GET after disabling public-read: %d", w.Code)
	}
}

func TestIntegrationPublicReadSignedExplicitDeny(t *testing.T) {
	f := newPubFixture(t, 0)
	f.cfg.Server.S3PublicEndpoint = "http://s3.bkt.test"
	pub := "/" + f.pub.Name
	denyGet := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/*"]}]}`, f.pub.Name)
	dst := itBucket(t, f.admin.ID, nil)
	allowDst := itPolicy(t, fmt.Sprintf(
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:PutObject"],"Resource":["arn:aws:s3:::%s/*"]}]}`, dst.Name))

	// assertDenied checks every read path for c on hello.txt.
	assertDenied := func(t *testing.T, what string, c *signedPubCaller) {
		t.Helper()
		if w := c.s3(t, http.MethodGet, pub+"/hello.txt", nil); w.Code != http.StatusForbidden {
			t.Errorf("%s: S3 GET %d, want 403", what, w.Code)
		}
		if w := c.s3(t, http.MethodHead, pub+"/hello.txt", nil); w.Code != http.StatusForbidden {
			t.Errorf("%s: S3 HEAD %d, want 403", what, w.Code)
		}
		if w := itDo(c.console, http.MethodGet, objectURL(f.pub.Name, "hello.txt"), nil); w.Code != http.StatusForbidden {
			t.Errorf("%s: REST download %d, want 403", what, w.Code)
		}
		if w := itDo(c.console, http.MethodHead, objectURL(f.pub.Name, "hello.txt"), nil); w.Code != http.StatusForbidden {
			t.Errorf("%s: REST head %d, want 403", what, w.Code)
		}
		if code, _ := c.presign(t, f.pub.Name, "hello.txt"); code != http.StatusForbidden {
			t.Errorf("%s: presign %d, want 403", what, code)
		}
		if w := c.s3(t, http.MethodPut, "/"+dst.Name+"/copy.txt", map[string]string{"X-Amz-Copy-Source": pub + "/hello.txt"}); w.Code != http.StatusForbidden {
			t.Errorf("%s: CopyObject %d, want 403", what, w.Code)
		}
	}

	userDeny := newSignedPubCaller(t, f, "pud")
	attachUserPolicy(t, userDeny.user, itPolicy(t, denyGet))
	attachUserPolicy(t, userDeny.user, allowDst)
	assertDenied(t, "user-policy Deny", userDeny)

	groupDeny := newSignedPubCaller(t, f, "pgd")
	attachGroupPolicy(t, groupDeny.user, itPolicy(t, denyGet))
	attachUserPolicy(t, groupDeny.user, allowDst)
	assertDenied(t, "group-policy Deny", groupDeny)

	// Bucket-policy Denies: one naming a user applies to that user only; one
	// for "*" applies to everyone.
	named := newSignedPubCaller(t, f, "pbn")
	other := newSignedPubCaller(t, f, "pbo")
	attachUserPolicy(t, named.user, allowDst)
	attachUserPolicy(t, other.user, allowDst)
	bp := models.BucketPolicy{BucketID: f.pub.ID, PolicyDocument: fmt.Sprintf(
		`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":[%q],"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/*"]}]}`,
		named.user.Username, f.pub.Name)}
	if err := database.DB.Create(&bp).Error; err != nil {
		t.Fatal(err)
	}
	assertDenied(t, "bucket-policy Deny naming the user", named)
	if w := other.s3(t, http.MethodGet, pub+"/hello.txt", nil); w.Code != http.StatusOK || w.Body.String() != f.content {
		t.Errorf("bucket-policy Deny naming another user: S3 GET %d, want 200", w.Code)
	}
	if w := itDo(other.console, http.MethodGet, objectURL(f.pub.Name, "hello.txt"), nil); w.Code != http.StatusOK {
		t.Errorf("bucket-policy Deny naming another user: REST download %d, want 200", w.Code)
	}

	database.DB.Model(&models.BucketPolicy{}).Where("bucket_id = ?", f.pub.ID).Update("policy_document", fmt.Sprintf(
		`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/*"]}]}`, f.pub.Name))
	assertDenied(t, `bucket-policy Deny for "*"`, other)
	// Anonymous reads are denied by it as well (unchanged behavior).
	if w := f.anon(http.MethodGet, pub+"/hello.txt", nil); w.Code != http.StatusForbidden {
		t.Errorf(`anonymous GET under a "*" Deny: %d`, w.Code)
	}

	// Admins are unaffected by all of it.
	adminKey := itAccessKey(t, f.admin, "pad-"+uuid.NewString()[:6], nil)
	admin := &signedPubCaller{f: f, user: f.admin, key: adminKey, secret: "secret-" + adminKey.Name}
	if w := admin.s3(t, http.MethodGet, pub+"/hello.txt", nil); w.Code != http.StatusOK {
		t.Errorf("admin S3 GET: %d", w.Code)
	}
}
