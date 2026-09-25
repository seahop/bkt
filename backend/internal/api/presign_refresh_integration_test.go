package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/security"

	"github.com/google/uuid"
)

// itAccessKey inserts an active access key for u (IsActive defaults to true).
func itAccessKey(t *testing.T, u models.User, name string, mutate func(*models.AccessKey)) models.AccessKey {
	t.Helper()
	if os.Getenv("ENCRYPTION_KEY") == "" {
		os.Setenv("ENCRYPTION_KEY", "presign-test-encryption-key-0123456789abcdef")
	}
	enc, err := security.EncryptSecretKey("secret-" + name)
	if err != nil {
		t.Fatal(err)
	}
	k := models.AccessKey{
		ID: uuid.New(), UserID: u.ID, AccessKey: "AK" + strings.ReplaceAll(uuid.NewString(), "-", "")[:18],
		SecretKeyHash: "x", SecretKeyEncrypted: enc, Name: name, IsActive: true,
	}
	if mutate != nil {
		mutate(&k)
	}
	if err := database.DB.Create(&k).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DB.Exec(`DELETE FROM access_keys WHERE id = ?`, k.ID) })
	return k
}

func presignAs(t *testing.T, u models.User, bucket, key string) *httptest.ResponseRecorder {
	t.Helper()
	cfg := itConfig(t)
	h := NewBucketHandler(cfg)
	r := itRouter(u)
	r.POST("/api/buckets/:name/objects/presign", h.PresignObject)
	body, _ := json.Marshal(map[string]interface{}{"key": key, "expires_in": 3600})
	return itDo(r, http.MethodPost, "/api/buckets/"+bucket+"/objects/presign", body)
}

func TestIntegrationPresignSigningKeySelection(t *testing.T) {
	integrationDB(t)
	u := mkUser(t, "ps-"+uuid.NewString()[:8], "password-123", true, false)
	b := itBucket(t, u.ID, nil)
	if err := database.DB.Create(&models.Object{BucketID: b.ID, Key: "hello.txt", Size: 5}).Error; err != nil {
		t.Fatal(err)
	}

	signer := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		var resp map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		name, _ := resp["signing_key_name"].(string)
		return name
	}

	// Only a revoked STS credential (the reported bug: its link returned
	// "access key ... has been revoked"): no usable key → 409, never a dead link.
	stale := 0
	itAccessKey(t, u, "sts-stale", func(k *models.AccessKey) {
		exp := time.Now().Add(6 * time.Hour)
		k.Temporary, k.ExpiresAt, k.IssuerTokenVersion = true, &exp, &stale
	})
	database.DB.Model(&models.User{}).Where("id = ?", u.ID).Update("token_version", 3)
	if w := presignAs(t, u, b.Name, "hello.txt"); w.Code != http.StatusConflict {
		t.Fatalf("only a revoked STS key: got %d %s, want 409", w.Code, w.Body.String())
	}

	// A valid STS credential is still not used for share links.
	current := 3
	itAccessKey(t, u, "sts-valid", func(k *models.AccessKey) {
		exp := time.Now().Add(6 * time.Hour)
		k.Temporary, k.ExpiresAt, k.IssuerTokenVersion = true, &exp, &current
	})
	if w := presignAs(t, u, b.Name, "hello.txt"); w.Code != http.StatusConflict {
		t.Fatalf("only STS keys: got %d %s, want 409", w.Code, w.Body.String())
	}

	// Expired long-lived key is skipped; of two expiring keys the longest-lived wins.
	itAccessKey(t, u, "expired", func(k *models.AccessKey) {
		exp := time.Now().Add(-time.Hour)
		k.ExpiresAt = &exp
	})
	itAccessKey(t, u, "short", func(k *models.AccessKey) {
		exp := time.Now().Add(2 * time.Hour)
		k.ExpiresAt = &exp
	})
	itAccessKey(t, u, "long", func(k *models.AccessKey) {
		exp := time.Now().Add(30 * 24 * time.Hour)
		k.ExpiresAt = &exp
	})
	if w := presignAs(t, u, b.Name, "hello.txt"); w.Code != http.StatusOK || signer(w) != "long" {
		t.Fatalf("expiring keys: got %d signer=%q %s, want 200 signed by \"long\"", w.Code, signer(w), w.Body.String())
	}

	// A non-expiring key beats every expiring one.
	itAccessKey(t, u, "forever", nil)
	if w := presignAs(t, u, b.Name, "hello.txt"); w.Code != http.StatusOK || signer(w) != "forever" {
		t.Fatalf("non-expiring key: got %d signer=%q, want 200 signed by \"forever\"", w.Code, signer(w))
	}
}

func TestIntegrationRefreshReuseGraceWindow(t *testing.T) {
	r := consoleCookieRouter(t)
	name := "rg-" + uuid.NewString()[:8]
	u := mkUser(t, name, "password-123", false, false)

	w := sendAuth(r, authReq{path: "/api/auth/login", body: map[string]string{"username": name, "password": "password-123"}})
	if w.Code != http.StatusOK {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	first, _ := bodyMap(t, w)["refresh_token"].(string)
	if first == "" {
		t.Fatal("API login returned no refresh_token")
	}
	tokenVersion := func() int {
		var got models.User
		database.DB.First(&got, "id = ?", u.ID)
		return got.TokenVersion
	}
	before := tokenVersion()

	// Normal rotation.
	if w := sendAuth(r, authReq{path: "/api/auth/refresh", body: map[string]string{"refresh_token": first}}); w.Code != http.StatusOK {
		t.Fatalf("first refresh: %d %s", w.Code, w.Body.String())
	}
	// Immediate replay of the rotated token (benign race): still served, no
	// session-wide revocation.
	if w := sendAuth(r, authReq{path: "/api/auth/refresh", body: map[string]string{"refresh_token": first}}); w.Code != http.StatusOK {
		t.Fatalf("replay within grace: %d %s, want 200", w.Code, w.Body.String())
	}
	if tokenVersion() != before {
		t.Fatal("replay within the grace window revoked all sessions")
	}

	// Age the rotation past the window: a replay is now treated as theft.
	database.DB.Exec(`UPDATE revoked_tokens SET created_at = NOW() - INTERVAL '5 minutes' WHERE user_id = ?`, u.ID)
	if w := sendAuth(r, authReq{path: "/api/auth/refresh", body: map[string]string{"refresh_token": first}}); w.Code != http.StatusUnauthorized {
		t.Fatalf("replay after grace: %d %s, want 401", w.Code, w.Body.String())
	}
	if tokenVersion() != before+1 {
		t.Fatalf("replay after grace should bump token_version (%d → %d)", before, tokenVersion())
	}
}
