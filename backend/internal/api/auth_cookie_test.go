package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bkt/internal/config"
	"bkt/internal/services"

	"github.com/gin-gonic/gin"
)

// Unit tests for the console-cookie session paths that never reach the
// database (validation and header gating). The full round trips live in
// auth_cookie_integration_test.go.

func cookieUnitRouter(cfg *config.Config) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := &AuthHandler{config: cfg, loginGuard: newLoginGuard(), auditService: services.NewAuditService()}
	uh := NewUserHandler(cfg)
	r := gin.New()
	r.POST("/api/auth/refresh", h.RefreshToken)
	r.POST("/api/auth/register", h.Register)
	r.POST("/api/users", uh.CreateUser)
	return r
}

func doAuthRequest(r http.Handler, path, body string, console bool, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if console {
		req.Header.Set("X-Bkt-Client", "console")
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "bkt_refresh", Value: cookie})
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestRefreshWithCookieRequiresConsoleHeader(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "0123456789abcdef0123456789abcdef"
	r := cookieUnitRouter(cfg)

	// Cookie but no X-Bkt-Client: refused before the token is even looked at.
	w := doAuthRequest(r, "/api/auth/refresh", "", false, "some.refresh.token")
	if w.Code != http.StatusForbidden {
		t.Fatalf("cookie without console header: %d %s, want 403", w.Code, w.Body.String())
	}
	if len(w.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("a refused refresh must not touch the cookie: %q", w.Header().Values("Set-Cookie"))
	}
}

func TestRefreshWithoutAnyTokenIs400(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "0123456789abcdef0123456789abcdef"
	r := cookieUnitRouter(cfg)

	for _, tc := range []struct {
		name, body string
		console    bool
	}{
		{"empty body, no cookie", "", true},
		{"empty object", "{}", false},
		{"malformed json", "{", true},
	} {
		if w := doAuthRequest(r, "/api/auth/refresh", tc.body, tc.console, ""); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", tc.name, w.Code, w.Body.String())
		}
	}
}

func TestRefreshWithInvalidCookieIs401AndKeepsCookie(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "0123456789abcdef0123456789abcdef"
	r := cookieUnitRouter(cfg)
	w := doAuthRequest(r, "/api/auth/refresh", "", true, "not-a-jwt")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("garbage cookie: %d, want 401", w.Code)
	}
	// Never clear on failure: another tab may have just rotated the cookie,
	// and clearing it here would log that tab out.
	if len(w.Header().Values("Set-Cookie")) != 0 {
		t.Fatalf("failed refresh must not clear the cookie: %q", w.Header().Values("Set-Cookie"))
	}
}

// bcrypt only accepts 72 bytes; longer passwords are a 400, not a 500. The
// limit is in bytes, so 40 three-byte characters (120 bytes) are refused.
func TestNewPasswordOver72BytesRejected(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.AllowRegistration = true
	cfg.Auth.BcryptCost = 4
	r := cookieUnitRouter(cfg)

	for name, pw := range map[string]string{
		"73 ascii":            strings.Repeat("a", 73),
		"40 multi-byte runes": strings.Repeat("€", 40),
	} {
		reg, _ := json.Marshal(map[string]string{"username": "someone", "email": "someone@example.test", "password": pw})
		if w := doAuthRequest(r, "/api/auth/register", string(reg), true, ""); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "72 bytes") {
			t.Errorf("register %s: %d %s, want 400 mentioning 72 bytes", name, w.Code, w.Body.String())
		}
		create, _ := json.Marshal(map[string]interface{}{"username": "someone", "email": "someone@example.test", "password": pw})
		if w := doAuthRequest(r, "/api/users", string(create), true, ""); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "72 bytes") {
			t.Errorf("create user %s: %d %s, want 400 mentioning 72 bytes", name, w.Code, w.Body.String())
		}
	}
}
