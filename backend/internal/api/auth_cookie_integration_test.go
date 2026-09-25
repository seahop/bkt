package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bkt/internal/database"
	"bkt/internal/middleware"
	"bkt/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Console session over the httpOnly bkt_refresh cookie, against a real
// Postgres (see integrationDB). Run with BKT_TEST_POSTGRES_HOST set.

func consoleCookieRouter(t *testing.T) *gin.Engine {
	t.Helper()
	cfg := integrationDB(t)
	cfg.Auth.AllowRegistration = true
	cfg.TLS.Enabled = true
	gin.SetMode(gin.TestMode)
	h := &AuthHandler{config: cfg, loginGuard: newLoginGuard(), auditService: services.NewAuditService()}
	uh := NewUserHandler(cfg)
	r := gin.New()
	r.POST("/api/auth/login", h.Login)
	r.POST("/api/auth/register", h.Register)
	r.POST("/api/auth/refresh", h.RefreshToken)
	r.POST("/api/auth/logout", middleware.AuthMiddleware(cfg.Auth.JWTSecret), h.Logout)
	r.PUT("/api/users/me", middleware.AuthMiddleware(cfg.Auth.JWTSecret), uh.UpdateCurrentUser)
	return r
}

type authReq struct {
	method, path string
	body         interface{}
	console      bool
	cookie       string
	bearer       string
}

func sendAuth(r http.Handler, a authReq) *httptest.ResponseRecorder {
	var rd *strings.Reader
	if a.body != nil {
		b, _ := json.Marshal(a.body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	method := a.method
	if method == "" {
		method = http.MethodPost
	}
	req := httptest.NewRequest(method, a.path, rd)
	if a.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.console {
		req.Header.Set("X-Bkt-Client", "console")
	}
	if a.cookie != "" {
		req.AddCookie(&http.Cookie{Name: "bkt_refresh", Value: a.cookie})
	}
	if a.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+a.bearer)
	}
	req.RemoteAddr = "192.0.2.77:1234"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// refreshCookieOf returns the bkt_refresh cookie set by the response, failing
// the test when there is none.
func refreshCookieOf(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range (&http.Response{Header: w.Header()}).Cookies() {
		if c.Name == "bkt_refresh" {
			return c
		}
	}
	t.Fatalf("no bkt_refresh cookie in %q", w.Header().Values("Set-Cookie"))
	return nil
}

func assertSessionCookie(t *testing.T, c *http.Cookie) {
	t.Helper()
	if c.Value == "" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/api/auth" || c.Domain != "" || c.MaxAge != 24*3600 {
		t.Fatalf("bad session cookie: %+v", c)
	}
}

func bodyMap(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return m
}

func TestIntegrationConsoleLoginUsesCookieOnly(t *testing.T) {
	r := consoleCookieRouter(t)
	name := "ck-" + uuid.NewString()[:8]
	mkUser(t, name, "correct-horse-battery", false, false)
	creds := map[string]string{"username": name, "password": "correct-horse-battery"}

	// Console: refresh token only in the cookie.
	w := sendAuth(r, authReq{path: "/api/auth/login", body: creds, console: true})
	if w.Code != http.StatusOK {
		t.Fatalf("console login: %d %s", w.Code, w.Body.String())
	}
	m := bodyMap(t, w)
	if _, has := m["refresh_token"]; has {
		t.Fatalf("console login body must not carry refresh_token: %s", w.Body.String())
	}
	if m["token"] == "" || m["token"] == nil {
		t.Fatalf("console login body lacks the access token: %s", w.Body.String())
	}
	assertSessionCookie(t, refreshCookieOf(t, w))

	// API client (no header): unchanged, refresh_token in the body.
	w = sendAuth(r, authReq{path: "/api/auth/login", body: creds})
	if w.Code != http.StatusOK {
		t.Fatalf("api login: %d %s", w.Code, w.Body.String())
	}
	if rt, _ := bodyMap(t, w)["refresh_token"].(string); rt == "" {
		t.Fatalf("API login must keep refresh_token in the body: %s", w.Body.String())
	}
}

func TestIntegrationConsoleRegisterUsesCookieOnly(t *testing.T) {
	r := consoleCookieRouter(t)
	name := "ckr-" + uuid.NewString()[:8]
	t.Cleanup(func() { deleteUserNamed(name) })
	w := sendAuth(r, authReq{path: "/api/auth/register", console: true,
		body: map[string]string{"username": name, "email": name + "@example.test", "password": "correct-horse-battery"}})
	if w.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	if _, has := bodyMap(t, w)["refresh_token"]; has {
		t.Fatalf("console register body must not carry refresh_token: %s", w.Body.String())
	}
	assertSessionCookie(t, refreshCookieOf(t, w))
}

func TestIntegrationRefreshViaCookieRotatesCookie(t *testing.T) {
	r := consoleCookieRouter(t)
	name := "ckf-" + uuid.NewString()[:8]
	mkUser(t, name, "correct-horse-battery", false, false)
	w := sendAuth(r, authReq{path: "/api/auth/login", console: true,
		body: map[string]string{"username": name, "password": "correct-horse-battery"}})
	first := refreshCookieOf(t, w).Value

	// Cookie without the console header is refused (and does not burn the token).
	if w := sendAuth(r, authReq{path: "/api/auth/refresh", cookie: first}); w.Code != http.StatusForbidden {
		t.Fatalf("cookie refresh without header: %d, want 403", w.Code)
	}

	w = sendAuth(r, authReq{path: "/api/auth/refresh", console: true, cookie: first})
	if w.Code != http.StatusOK {
		t.Fatalf("cookie refresh: %d %s", w.Code, w.Body.String())
	}
	m := bodyMap(t, w)
	if tok, _ := m["token"].(string); tok == "" {
		t.Fatalf("refresh body lacks token: %s", w.Body.String())
	}
	if _, has := m["refresh_token"]; has {
		t.Fatalf("console refresh body must not carry refresh_token: %s", w.Body.String())
	}
	rotated := refreshCookieOf(t, w)
	assertSessionCookie(t, rotated)
	if rotated.Value == first {
		t.Fatal("refresh did not rotate the cookie")
	}

	// The rotated cookie works; the superseded one is a replay — tolerated
	// only within the reuse grace window (benign races), 401 after it.
	w = sendAuth(r, authReq{path: "/api/auth/refresh", console: true, cookie: rotated.Value})
	if w.Code != http.StatusOK {
		t.Fatalf("rotated cookie refresh: %d %s", w.Code, w.Body.String())
	}
	if w := sendAuth(r, authReq{path: "/api/auth/refresh", console: true, cookie: first}); w.Code != http.StatusOK {
		t.Fatalf("replayed cookie within grace: %d, want 200", w.Code)
	}
	database.DB.Exec(`UPDATE revoked_tokens SET created_at = NOW() - INTERVAL '5 minutes'`)
	if w := sendAuth(r, authReq{path: "/api/auth/refresh", console: true, cookie: first}); w.Code != http.StatusUnauthorized {
		t.Fatalf("replayed cookie after grace: %d, want 401", w.Code)
	}
}

func TestIntegrationRefreshViaBodyUnchangedForAPIClients(t *testing.T) {
	r := consoleCookieRouter(t)
	name := "ckb-" + uuid.NewString()[:8]
	mkUser(t, name, "correct-horse-battery", false, false)
	w := sendAuth(r, authReq{path: "/api/auth/login", body: map[string]string{"username": name, "password": "correct-horse-battery"}})
	rt, _ := bodyMap(t, w)["refresh_token"].(string)

	w = sendAuth(r, authReq{path: "/api/auth/refresh", body: map[string]string{"refresh_token": rt}})
	if w.Code != http.StatusOK {
		t.Fatalf("body refresh: %d %s", w.Code, w.Body.String())
	}
	next, _ := bodyMap(t, w)["refresh_token"].(string)
	if next == "" || next == rt {
		t.Fatalf("API refresh must return a rotated refresh_token in the body: %s", w.Body.String())
	}
}

func TestIntegrationLogoutRevokesAndClearsCookie(t *testing.T) {
	r := consoleCookieRouter(t)
	name := "ckl-" + uuid.NewString()[:8]
	mkUser(t, name, "correct-horse-battery", false, false)
	w := sendAuth(r, authReq{path: "/api/auth/login", console: true,
		body: map[string]string{"username": name, "password": "correct-horse-battery"}})
	access, _ := bodyMap(t, w)["token"].(string)
	cookie := refreshCookieOf(t, w).Value

	// Rotate once so the cookie is no longer the access token's sibling:
	// logout must still revoke it (it reads the cookie itself).
	w = sendAuth(r, authReq{path: "/api/auth/refresh", console: true, cookie: cookie})
	cookie = refreshCookieOf(t, w).Value

	w = sendAuth(r, authReq{path: "/api/auth/logout", console: true, cookie: cookie, bearer: access})
	if w.Code != http.StatusOK {
		t.Fatalf("logout: %d %s", w.Code, w.Body.String())
	}
	cleared := refreshCookieOf(t, w)
	if cleared.Value != "" || cleared.MaxAge >= 0 || cleared.Path != "/api/auth" {
		t.Fatalf("logout must clear the cookie (Max-Age=0, Path=/api/auth): %q", w.Header().Values("Set-Cookie"))
	}
	if w := sendAuth(r, authReq{path: "/api/auth/refresh", console: true, cookie: cookie}); w.Code != http.StatusUnauthorized {
		t.Fatalf("refresh after logout: %d, want 401", w.Code)
	}

	// A logout with no cookie still clears it.
	w = sendAuth(r, authReq{path: "/api/auth/login", body: map[string]string{"username": name, "password": "correct-horse-battery"}})
	access, _ = bodyMap(t, w)["token"].(string)
	w = sendAuth(r, authReq{path: "/api/auth/logout", bearer: access})
	if w.Code != http.StatusOK || refreshCookieOf(t, w).MaxAge >= 0 {
		t.Fatalf("cookie-less logout: %d %q", w.Code, w.Header().Values("Set-Cookie"))
	}
}

func TestIntegrationPasswordChangeOver72BytesIs400(t *testing.T) {
	r := consoleCookieRouter(t)
	name := "ckp-" + uuid.NewString()[:8]
	mkUser(t, name, "correct-horse-battery", false, false)
	w := sendAuth(r, authReq{path: "/api/auth/login", body: map[string]string{"username": name, "password": "correct-horse-battery"}})
	access, _ := bodyMap(t, w)["token"].(string)

	w = sendAuth(r, authReq{method: http.MethodPut, path: "/api/users/me", bearer: access,
		body: map[string]string{"current_password": "correct-horse-battery", "password": strings.Repeat("ü", 37)}}) // 74 bytes
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "72 bytes") {
		t.Fatalf("overlong new password: %d %s, want 400", w.Code, w.Body.String())
	}
	// Exactly 72 bytes is accepted.
	w = sendAuth(r, authReq{method: http.MethodPut, path: "/api/users/me", bearer: access,
		body: map[string]string{"current_password": "correct-horse-battery", "password": strings.Repeat("ü", 36)}})
	if w.Code != http.StatusOK {
		t.Fatalf("72-byte password: %d %s, want 200", w.Code, w.Body.String())
	}
}

func deleteUserNamed(name string) {
	database.DB.Exec(`DELETE FROM users WHERE username = ?`, name)
}
