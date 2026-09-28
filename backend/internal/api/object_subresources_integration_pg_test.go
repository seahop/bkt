package api

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bkt/internal/database"
	"bkt/internal/models"

	"github.com/google/uuid"
)

// Object-level sub-resources on the real S3 router: implemented ones keep
// working, ?acl is a read-only view, and every other query key is 501 —
// never object content, never a write or delete.

const objContent = "hello"

func objectExists(t *testing.T, bucketID uuid.UUID, key string) bool {
	t.Helper()
	_, ok := currentRow(t, bucketID, key)
	return ok
}

func TestIntegrationS3ObjectSubresourceRouting(t *testing.T) {
	f := newBPFixture(t)
	s3PutString(t, itS3Router(f.cfg, f.admin), f.pub.Name, "hello.txt", objContent)
	b := "/" + f.bucket.Name
	obj := b + "/public.txt" // content "hello" (see newBPFixture)
	pubObj := "/" + f.pub.Name + "/hello.txt"
	noContent := func(what string, w *httptest.ResponseRecorder) {
		t.Helper()
		if strings.Contains(w.Body.String(), objContent) && !strings.Contains(w.Body.String(), "<") {
			t.Errorf("%s: answered with object content: %q", what, w.Body.String())
		}
	}

	// ── GET ──
	for _, q := range []string{"attributes", "retention", "legal-hold", "torrent", "restore", "select", "foo", "uploadId=", "uploads", "versionId=null&foo=1", "tagging&foo"} {
		w := f.do(t, f.adminKey, http.MethodGet, obj+"?"+q, nil)
		noContent("GET ?"+q, w)
		expectS3Err(t, "GET ?"+q, w, http.StatusNotImplemented, "NotImplemented")
	}
	for _, q := range []string{"", "?x-id=GetObject", "?versionId=null", "?response-content-type=text/plain&response-cache-control=no-store"} {
		if w := f.do(t, f.adminKey, http.MethodGet, obj+q, nil); w.Code != http.StatusOK || w.Body.String() != objContent {
			t.Errorf("GET %s: %d %q", q, w.Code, w.Body.String())
		}
	}
	// partNumber: bkt objects are single-part.
	w := f.do(t, f.adminKey, http.MethodGet, obj+"?partNumber=1", nil)
	if w.Code != http.StatusPartialContent || w.Body.String() != objContent || w.Header().Get("Content-Range") != "bytes 0-4/5" {
		t.Errorf("GET ?partNumber=1: %d %q %q", w.Code, w.Body.String(), w.Header().Get("Content-Range"))
	}
	expectS3Err(t, "GET ?partNumber=2", f.do(t, f.adminKey, http.MethodGet, obj+"?partNumber=2", nil), http.StatusRequestedRangeNotSatisfiable, "InvalidPartNumber")
	expectS3Err(t, "GET ?partNumber=x", f.do(t, f.adminKey, http.MethodGet, obj+"?partNumber=x", nil), http.StatusBadRequest, "InvalidArgument")

	// ?acl: read-only ACL with the object-read authorization.
	w = f.do(t, f.adminKey, http.MethodGet, obj+"?acl", nil)
	if body := w.Body.String(); w.Code != http.StatusOK || !strings.Contains(body, "<AccessControlPolicy") ||
		!strings.Contains(body, "FULL_CONTROL") || strings.Contains(body, "AllUsers") {
		t.Errorf("GET ?acl (private): %d %s", w.Code, body)
	}
	if body := f.do(t, f.adminKey, http.MethodGet, pubObj+"?acl", nil).Body.String(); !strings.Contains(body, "AllUsers") {
		t.Errorf("GET ?acl (public): %s", body)
	}
	if w := f.do(t, f.userKey, http.MethodGet, obj+"?acl", nil); w.Code != http.StatusOK {
		t.Errorf("GET ?acl as a reader: %d %s", w.Code, w.Body.String())
	}
	expectS3Err(t, "GET ?acl without read access", f.do(t, f.nobodyKey, http.MethodGet, obj+"?acl", nil), http.StatusForbidden, "AccessDenied")
	// Public-read covers ?acl like the object read itself.
	if w := f.do(t, f.nobodyKey, http.MethodGet, pubObj+"?acl", nil); w.Code != http.StatusOK {
		t.Errorf("GET ?acl on a public bucket without grants: %d %s", w.Code, w.Body.String())
	}
	expectS3Err(t, "GET ?acl missing key", f.do(t, f.adminKey, http.MethodGet, b+"/nope?acl", nil), http.StatusNotFound, "NoSuchKey")
	expectS3Err(t, "GET ?acl unknown version", f.do(t, f.adminKey, http.MethodGet, obj+"?acl&versionId=nope", nil), http.StatusNotFound, "NoSuchVersion")

	// ?tagging still works; another version's tags are not silently the current one's.
	if w := f.do(t, f.adminKey, http.MethodPut, obj+"?tagging", []byte(`<Tagging><TagSet><Tag><Key>k</Key><Value>v</Value></Tag></TagSet></Tagging>`)); w.Code != http.StatusOK {
		t.Fatalf("PUT ?tagging: %d %s", w.Code, w.Body.String())
	}
	for _, q := range []string{"tagging", "tagging&versionId=null"} {
		if w := f.do(t, f.adminKey, http.MethodGet, obj+"?"+q, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<Key>k</Key>") {
			t.Errorf("GET ?%s: %d %s", q, w.Code, w.Body.String())
		}
	}
	expectS3Err(t, "GET ?tagging&versionId=other", f.do(t, f.adminKey, http.MethodGet, obj+"?tagging&versionId=other", nil), http.StatusNotImplemented, "NotImplemented")
	expectS3Err(t, "PUT ?tagging&versionId=other", f.do(t, f.adminKey, http.MethodPut, obj+"?tagging&versionId=other", []byte(`<Tagging><TagSet></TagSet></Tagging>`)), http.StatusNotImplemented, "NotImplemented")

	// ── HEAD ──
	for _, q := range []string{"acl", "attributes", "foo", "tagging"} {
		w := f.do(t, f.adminKey, http.MethodHead, obj+"?"+q, nil)
		if w.Code != http.StatusNotImplemented || w.Body.Len() != 0 {
			t.Errorf("HEAD ?%s: %d %q, want 501 with no body", q, w.Code, w.Body.String())
		}
	}
	for q, want := range map[string]int{"": 200, "?versionId=null": 200, "?partNumber=1": 200, "?partNumber=2": 416, "?x-id=HeadObject": 200} {
		if w := f.do(t, f.adminKey, http.MethodHead, obj+q, nil); w.Code != want {
			t.Errorf("HEAD %s: %d, want %d", q, w.Code, want)
		}
	}

	// ── PUT: nothing is written for an unsupported sub-resource ──
	for _, q := range []string{"acl", "retention", "legal-hold", "foo", "versionId=x", "restore"} {
		for _, target := range []string{obj, b + "/new-" + strings.NewReplacer("=", "", "-", "").Replace(q)} {
			w := f.do(t, f.adminKey, http.MethodPut, target+"?"+q, []byte("OVERWRITTEN"))
			expectS3Err(t, "PUT "+target+"?"+q, w, http.StatusNotImplemented, "NotImplemented")
		}
	}
	if w := f.do(t, f.adminKey, http.MethodPut, obj+"?acl", nil); !strings.Contains(w.Body.String(), "Public read access") {
		t.Errorf("PUT ?acl message: %s", w.Body.String())
	}
	for _, q := range []string{"uploadId=abc", "partNumber=1", "uploadId=&partNumber=1", "uploadId=abc&partNumber="} {
		expectS3Err(t, "PUT ?"+q, f.do(t, f.adminKey, http.MethodPut, b+"/half?"+q, []byte("OVERWRITTEN")), http.StatusBadRequest, "InvalidArgument")
	}
	if code, body := s3GetString(t, itS3Router(f.cfg, f.admin), f.bucket.Name, "public.txt"); code != 200 || body != objContent {
		t.Errorf("object changed by refused PUTs: %d %q", code, body)
	}
	var n int64
	database.DB.Model(&models.Object{}).Where("bucket_id = ? AND (key LIKE 'new%' OR key = 'half')", f.bucket.ID).Count(&n)
	if n != 0 {
		t.Errorf("refused PUTs created %d objects", n)
	}
	if w := f.do(t, f.adminKey, http.MethodPut, b+"/plain.txt?x-id=PutObject", []byte("plain")); w.Code != http.StatusOK {
		t.Errorf("PUT ?x-id=PutObject: %d %s", w.Code, w.Body.String())
	}

	// ── DELETE: nothing is deleted for an unsupported sub-resource ──
	for _, q := range []string{"retention", "legal-hold", "foo", "acl", "uploadId="} {
		expectS3Err(t, "DELETE ?"+q, f.do(t, f.adminKey, http.MethodDelete, obj+"?"+q, nil), http.StatusNotImplemented, "NotImplemented")
	}
	if !objectExists(t, f.bucket.ID, "public.txt") {
		t.Fatal("object deleted by a refused DELETE")
	}
	if w := f.do(t, f.adminKey, http.MethodDelete, obj+"?tagging", nil); w.Code != http.StatusNoContent {
		t.Errorf("DELETE ?tagging: %d", w.Code)
	}
	if w := f.do(t, f.adminKey, http.MethodDelete, b+"/plain.txt?versionId=null", nil); w.Code != http.StatusNoContent || objectExists(t, f.bucket.ID, "plain.txt") {
		t.Errorf("DELETE ?versionId=null: %d", w.Code)
	}

	// ── POST ──
	for _, q := range []string{"restore", "select&select-type=2", "foo", ""} {
		expectS3Err(t, "POST ?"+q, f.do(t, f.adminKey, http.MethodPost, obj+"?"+q, nil), http.StatusNotImplemented, "NotImplemented")
	}

	// ── Anonymous: the public-read whitelist is unchanged ──
	for _, c := range []struct{ method, target string }{
		{http.MethodGet, pubObj + "?acl"}, {http.MethodGet, pubObj + "?attributes"}, {http.MethodGet, pubObj + "?partNumber=1"},
		{http.MethodHead, pubObj + "?acl"}, {http.MethodPut, pubObj + "?acl"}, {http.MethodDelete, pubObj + "?foo"},
		{http.MethodPost, pubObj + "?restore"},
	} {
		req := httptest.NewRequest(c.method, c.target, nil)
		rec := httptest.NewRecorder()
		f.s3.ServeHTTP(rec, req)
		assertUnsignedRejected(t, c.method+" "+c.target, rec)
	}
	req := httptest.NewRequest(http.MethodGet, pubObj, nil)
	rec := httptest.NewRecorder()
	f.s3.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != objContent {
		t.Errorf("anonymous public GET: %d %q", rec.Code, rec.Body.String())
	}
}

func TestIntegrationS3MultipartStillWorks(t *testing.T) {
	f := newBPFixture(t)
	b := "/" + f.bucket.Name
	key := b + "/mp.bin"

	create := func() string {
		w := f.do(t, f.adminKey, http.MethodPost, key+"?uploads", nil)
		var r struct {
			UploadID string `xml:"UploadId"`
		}
		if err := xml.Unmarshal(w.Body.Bytes(), &r); err != nil || w.Code != http.StatusOK || r.UploadID == "" {
			t.Fatalf("CreateMultipartUpload: %d %s", w.Code, w.Body.String())
		}
		return r.UploadID
	}
	id := create()
	w := f.do(t, f.adminKey, http.MethodPut, key+"?partNumber=1&uploadId="+id, []byte("multipart-body"))
	if w.Code != http.StatusOK || w.Header().Get("ETag") == "" {
		t.Fatalf("UploadPart: %d %s", w.Code, w.Body.String())
	}
	etag := w.Header().Get("ETag")
	if w := f.do(t, f.adminKey, http.MethodGet, key+"?uploadId="+id, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<PartNumber>1</PartNumber>") {
		t.Errorf("ListParts: %d %s", w.Code, w.Body.String())
	}
	complete := fmt.Sprintf(`<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>%s</ETag></Part></CompleteMultipartUpload>`, etag)
	if w := f.do(t, f.adminKey, http.MethodPost, key+"?uploadId="+id, []byte(complete)); w.Code != http.StatusOK {
		t.Fatalf("CompleteMultipartUpload: %d %s", w.Code, w.Body.String())
	}
	if w := f.do(t, f.adminKey, http.MethodGet, key, nil); w.Code != http.StatusOK || w.Body.String() != "multipart-body" {
		t.Errorf("GET after complete: %d %q", w.Code, w.Body.String())
	}
	// Abort.
	id2 := create()
	if w := f.do(t, f.adminKey, http.MethodDelete, key+"?uploadId="+id2, nil); w.Code != http.StatusNoContent {
		t.Errorf("AbortMultipartUpload: %d %s", w.Code, w.Body.String())
	}
	if !objectExists(t, f.bucket.ID, "mp.bin") {
		t.Error("abort removed the completed object")
	}
}

// GET /api/buckets/:name opens for any bucket-level read grant and reports
// what the caller may change, without leaking gated fields.
func TestIntegrationRESTGetBucketDetailsAuthz(t *testing.T) {
	f := newBPFixture(t)
	database.DB.Model(&models.Bucket{}).Where("id = ?", f.bucket.ID).Updates(map[string]interface{}{
		"webhook_url": "https://hooks.example.test/secret-token", "webhook_secret": "whsec", "webhook_events": "created",
		"replicate_to": f.pub.Name,
	})
	h := NewBucketHandler(f.cfg)
	get := func(u models.User) (int, map[string]interface{}, string) {
		r := itRouter(u)
		r.GET("/api/buckets/:name", h.GetBucket)
		w := itDo(r, http.MethodGet, "/api/buckets/"+f.bucket.Name, nil)
		var m map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &m)
		return w.Code, m, w.Body.String()
	}
	perms := func(m map[string]interface{}) map[string]interface{} {
		p, _ := m["permissions"].(map[string]interface{})
		return p
	}

	// Read-only template user (s3:GetObject + s3:ListBucket): allowed, all
	// settings read-only, gated fields absent.
	code, m, raw := get(f.user)
	if code != http.StatusOK {
		t.Fatalf("read-only user: %d %s", code, raw)
	}
	for k, v := range perms(m) {
		if v != false {
			t.Errorf("read-only user: permission %s = %v", k, v)
		}
	}
	if len(perms(m)) != 9 {
		t.Errorf("read-only user: permissions %v", perms(m))
	}
	for _, leak := range []string{"secret-token", "whsec", "webhook_url", "replicate_to", "s3_config", "email", "password", "is_admin"} {
		if strings.Contains(raw, leak) {
			t.Errorf("read-only user view leaks %q: %s", leak, raw)
		}
	}
	// s3:GetBucketPolicy alone also opens it.
	if code, m, raw := get(f.rdr); code != http.StatusOK || perms(m)["get_policy"] != true {
		t.Errorf("GetBucketPolicy grantee: %d %s", code, raw)
	}
	// No bucket-level read grant: still refused.
	nobody := mkUser(t, itName("gbn"), "password-123", false, false)
	grantPolicy(t, nobody, fmt.Sprintf(`[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::%s/*"]}]`, f.bucket.Name))
	if code, _, _ := get(nobody); code != http.StatusForbidden {
		t.Errorf("object-only grantee: %d, want 403", code)
	}
	// A configuration grant is reported (and reveals its field).
	cfgUser := mkUser(t, itName("gbc"), "password-123", false, false)
	grantPolicy(t, cfgUser, fmt.Sprintf(`[{"Effect":"Allow","Action":["s3:ListBucket","s3:PutBucketVersioning","s3:PutBucketNotification"],"Resource":["arn:aws:s3:::%s"]}]`, f.bucket.Name))
	code, m, raw = get(cfgUser)
	if code != http.StatusOK || perms(m)["put_versioning"] != true || perms(m)["put_notification"] != true ||
		perms(m)["put_lifecycle"] != false || perms(m)["put_policy"] != false || m["webhook_url"] != "https://hooks.example.test/secret-token" ||
		strings.Contains(raw, "whsec") || strings.Contains(raw, "replicate_to") {
		t.Errorf("config user: %d %s", code, raw)
	}
	// Admin: everything.
	code, m, _ = get(f.admin)
	if code != http.StatusOK {
		t.Fatalf("admin: %d", code)
	}
	for k, v := range perms(m) {
		if v != true {
			t.Errorf("admin: permission %s = %v", k, v)
		}
	}
}
