package auth

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"bkt/internal/config"

	"github.com/gin-gonic/gin"
)

func cookieTestContext(t *testing.T, target string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	return c, w
}

func onlyCookie(t *testing.T, w *httptest.ResponseRecorder) (*http.Cookie, string) {
	t.Helper()
	raw := w.Header().Values("Set-Cookie")
	if len(raw) != 1 {
		t.Fatalf("want exactly one Set-Cookie, got %q", raw)
	}
	cookies := (&http.Response{Header: w.Header()}).Cookies()
	if len(cookies) != 1 {
		t.Fatalf("unparseable Set-Cookie %q", raw[0])
	}
	return cookies[0], raw[0]
}

func TestSetRefreshCookieAttributes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		tls, proxy bool
		wantSecure bool
	}{
		{"plain http", false, false, false},
		{"TLS_ENABLED", true, false, true},
		{"TLS_TERMINATED_UPSTREAM", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Auth.RefreshTokenExpiry = "168h"
			cfg.TLS.Enabled, cfg.TLS.TerminatedUpstream = tc.tls, tc.proxy
			c, w := cookieTestContext(t, "/api/auth/login")

			SetRefreshCookie(c, cfg, "the-refresh-token")

			ck, raw := onlyCookie(t, w)
			if ck.Name != "bkt_refresh" || ck.Value != "the-refresh-token" {
				t.Fatalf("cookie %s=%s", ck.Name, ck.Value)
			}
			if !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/api/auth" {
				t.Fatalf("wrong attributes: %q", raw)
			}
			if ck.MaxAge != 7*24*3600 {
				t.Fatalf("Max-Age = %d, want the refresh lifetime (604800): %q", ck.MaxAge, raw)
			}
			if strings.Contains(strings.ToLower(raw), "domain=") {
				t.Fatalf("cookie must be host-only: %q", raw)
			}
			if ck.Secure != tc.wantSecure {
				t.Fatalf("Secure = %v, want %v: %q", ck.Secure, tc.wantSecure, raw)
			}
		})
	}
}

func TestSetRefreshCookieSecureOnDirectTLSRequest(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.RefreshTokenExpiry = "1h"
	c, w := cookieTestContext(t, "/api/auth/login")
	c.Request.TLS = &tls.ConnectionState{}
	SetRefreshCookie(c, cfg, "x")
	if ck, raw := onlyCookie(t, w); !ck.Secure {
		t.Fatalf("request over TLS must get a Secure cookie: %q", raw)
	}
}

func TestClearRefreshCookie(t *testing.T) {
	cfg := &config.Config{}
	c, w := cookieTestContext(t, "/api/auth/logout")
	ClearRefreshCookie(c, cfg)
	ck, raw := onlyCookie(t, w)
	if ck.Name != "bkt_refresh" || ck.Value != "" || ck.Path != "/api/auth" || !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode {
		t.Fatalf("clear cookie attributes: %q", raw)
	}
	if !strings.Contains(raw, "Max-Age=0") {
		t.Fatalf("clear cookie must carry Max-Age=0: %q", raw)
	}
}

func TestIsConsoleClient(t *testing.T) {
	for v, want := range map[string]bool{"console": true, "Console": true, "": false, "cli": false} {
		c, _ := cookieTestContext(t, "/api/auth/refresh")
		if v != "" {
			c.Request.Header.Set("X-Bkt-Client", v)
		}
		if got := IsConsoleClient(c); got != want {
			t.Errorf("IsConsoleClient(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestRefreshTokenFromCookie(t *testing.T) {
	c, _ := cookieTestContext(t, "/api/auth/refresh")
	if got := RefreshTokenFromCookie(c); got != "" {
		t.Fatalf("no cookie: got %q", got)
	}
	c.Request.AddCookie(&http.Cookie{Name: "bkt_refresh", Value: "abc"})
	if got := RefreshTokenFromCookie(c); got != "abc" {
		t.Fatalf("got %q, want abc", got)
	}
}

// Browser SSO completion: the refresh token goes only into the httpOnly
// cookie, never into the redirect URL (fragment or query).
func TestCompleteBrowserSSOKeepsRefreshTokenOutOfURL(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.RefreshTokenExpiry = "24h"
	cfg.TLS.Enabled = true
	c, w := cookieTestContext(t, "/api/auth/oidc/callback?code=x&state=y")

	completeBrowserSSO(c, cfg, "https://bkt.example/auth/oidc/callback", "ACCESS.TOKEN", "REFRESH.TOKEN")

	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status %d", w.Code)
	}
	loc := w.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme+"://"+u.Host+u.Path != "https://bkt.example/auth/oidc/callback" || u.RawQuery != "" {
		t.Fatalf("unexpected redirect %q", loc)
	}
	frag, _ := url.ParseQuery(u.Fragment)
	if frag.Get("token") != "ACCESS.TOKEN" {
		t.Fatalf("fragment must carry the access token: %q", loc)
	}
	if frag.Has("refresh_token") || strings.Contains(loc, "REFRESH.TOKEN") {
		t.Fatalf("refresh token leaked into the redirect URL: %q", loc)
	}
	ck, raw := onlyCookie(t, w)
	if ck.Name != "bkt_refresh" || ck.Value != "REFRESH.TOKEN" || !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/api/auth" {
		t.Fatalf("SSO refresh cookie: %q", raw)
	}
}
