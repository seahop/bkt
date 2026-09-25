package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"bkt/internal/auth"
	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Integration tests against a throwaway PostgreSQL. Run with e.g.
//
//	BKT_TEST_POSTGRES_HOST=pg BKT_TEST_POSTGRES_PASSWORD=t go test ./internal/api -run Integration
//
// The package's tests share one freshly created database (see
// integration_pg_test.go / pgtest), dropped when the test binary exits.
func integrationDB(t *testing.T) *config.Config {
	t.Helper()
	s, dbName := packageTestDatabase(t)
	cfg := &config.Config{}
	cfg.Database = s.DatabaseConfig(dbName)
	cfg.Auth.JWTSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cfg.Auth.BcryptCost = 4
	cfg.Auth.AccessTokenExpiry, cfg.Auth.RefreshTokenExpiry = "15m", "24h"
	if database.DB == nil {
		if err := database.Initialize(cfg); err != nil {
			t.Fatalf("db init: %v", err)
		}
	}
	return cfg
}

func mkUser(t *testing.T, name, password string, admin, locked bool) models.User {
	t.Helper()
	hash, _ := auth.HashPassword(password, 4)
	u := models.User{ID: uuid.New(), Username: name, Email: name + "@example.test", Password: hash, IsAdmin: admin, IsLocked: locked}
	if err := database.DB.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DB.Exec(`DELETE FROM users WHERE id = ?`, u.ID) })
	return u
}

func postJSON(r http.Handler, path, remote string, body interface{}) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if remote != "" {
		req.RemoteAddr = remote + ":1234"
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// The review's attack: failures from ~10 IPs keep the per-username throttle
// armed; the admin must still log in with the correct password.
func TestIntegrationLoginThrottleAdmitsCorrectPassword(t *testing.T) {
	cfg := integrationDB(t)
	name := "thr-" + uuid.NewString()[:8]
	mkUser(t, name, "correct-horse-battery", true, false)

	g := defaultLoginGuard()
	g.globalThreshold = 5
	g.baseBackoff, g.maxBackoff = 1<<40, 1<<40 // keep the throttle armed
	h := &AuthHandler{config: cfg, loginGuard: g, auditService: services.NewAuditService()}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/login", h.Login)

	for i := 0; i < 10; i++ {
		postJSON(r, "/login", fmt.Sprintf("198.51.100.%d", i), map[string]string{"username": name, "password": "wrong"})
	}
	if w := postJSON(r, "/login", "192.0.2.10", map[string]string{"username": name, "password": "wrong"}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("wrong password under throttle: %d, want 429", w.Code)
	}
	if w := postJSON(r, "/login", "192.0.2.10", map[string]string{"username": name, "password": "correct-horse-battery"}); w.Code != http.StatusOK {
		t.Fatalf("correct password under throttle: %d %s, want 200", w.Code, w.Body.String())
	}
	// Other sources remain throttled (the counter is not reset by a success).
	if w := postJSON(r, "/login", "203.0.113.9", map[string]string{"username": name, "password": "wrong"}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("other source after success: %d, want 429", w.Code)
	}
}

func TestIntegrationUserAdminGuards(t *testing.T) {
	cfg := integrationDB(t)
	uh := NewUserHandler(cfg)
	actorID := uuid.New()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user_id", actorID); c.Set("username", "actor"); c.Next() })
	r.POST("/users", uh.CreateUser)
	r.DELETE("/users/:id", uh.DeleteUser)
	r.POST("/users/:id/lock", uh.LockUser)

	// CreateUser refuses a username still named in a bucket policy.
	owner := mkUser(t, "own-"+uuid.NewString()[:8], "password-123", false, false)
	ghost := "ghost-" + uuid.NewString()[:8]
	b := models.Bucket{ID: uuid.New(), Name: "it-" + uuid.NewString()[:8], OwnerID: owner.ID}
	if err := database.DB.Create(&b).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		database.DB.Exec(`DELETE FROM bucket_policies WHERE bucket_id = ?`, b.ID)
		database.DB.Exec(`DELETE FROM buckets WHERE id = ?`, b.ID)
	})
	doc := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":[%q]},"Action":"s3:GetObject","Resource":"arn:aws:s3:::%s/*"}]}`, ghost, b.Name)
	if err := database.DB.Create(&models.BucketPolicy{BucketID: b.ID, PolicyDocument: doc}).Error; err != nil {
		t.Fatal(err)
	}
	w := postJSON(r, "/users", "", map[string]interface{}{"username": ghost, "email": ghost + "@example.test", "password": "password-123"})
	if w.Code != http.StatusConflict {
		t.Fatalf("CreateUser with policy-referenced username: %d %s, want 409", w.Code, w.Body.String())
	}
	var n int64
	database.DB.Model(&models.User{}).Where("username = ?", ghost).Count(&n)
	if n != 0 {
		t.Fatal("user was created")
	}

	// Last ACTIVE admin: locked admins don't count. Make sure no other
	// active admin exists in the shared test DB for this check.
	database.DB.Exec(`UPDATE users SET is_admin = false WHERE is_admin = true`)
	target := mkUser(t, "adm-"+uuid.NewString()[:8], "password-123", true, false)
	locked := mkUser(t, "lck-"+uuid.NewString()[:8], "password-123", true, true)
	req := httptest.NewRequest(http.MethodDelete, "/users/"+target.ID.String(), nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("deleting the last active admin (other admin locked): %d %s, want 409", w.Code, w.Body.String())
	}
	database.DB.Model(&models.User{}).Where("id = ?", locked.ID).Update("is_locked", false)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/users/"+target.ID.String(), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("deleting an admin with another active admin: %d %s, want 200", w.Code, w.Body.String())
	}

	// Admins cannot be locked (so the last active admin can't be either).
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/users/"+locked.ID.String()+"/lock", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("locking an admin: %d, want 403", w.Code)
	}
	// A regular user can be locked.
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/users/"+owner.ID.String()+"/lock", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("locking a user: %d %s", w.Code, w.Body.String())
	}
	var got models.User
	database.DB.First(&got, "id = ?", owner.ID)
	if !got.IsLocked || got.TokenVersion != owner.TokenVersion+1 {
		t.Fatalf("lock not applied: locked=%v tv=%d", got.IsLocked, got.TokenVersion)
	}
}
