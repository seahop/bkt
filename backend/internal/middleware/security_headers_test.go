package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRedactQuery(t *testing.T) {
	in := "X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKID%2F20260101%2Fus-east-1%2Fs3%2Faws4_request" +
		"&X-Amz-Signature=deadbeef&x-amz-security-token=sess&prefix=a%2Fb&code=authcode&state=xyz&Signature=v2sig&flag"
	out := RedactQuery(in)
	for _, secret := range []string{"deadbeef", "AKID", "sess", "authcode", "xyz", "v2sig"} {
		if strings.Contains(out, secret) {
			t.Errorf("redacted query still contains %q: %s", secret, out)
		}
	}
	for _, keep := range []string{"X-Amz-Algorithm=AWS4-HMAC-SHA256", "prefix=a%2Fb", "&flag"} {
		if !strings.Contains(out, keep) {
			t.Errorf("redacted query lost %q: %s", keep, out)
		}
	}
	if RedactQuery("") != "" {
		t.Error("empty query should stay empty")
	}
}

func TestIsActiveContentType(t *testing.T) {
	active := []string{"text/html", "text/html; charset=utf-8", "TEXT/HTML", "image/svg+xml", "application/xml",
		"text/xml", "application/xhtml+xml", "application/javascript", "text/javascript", "application/rss+xml"}
	passive := []string{"image/png", "application/pdf", "text/plain", "application/octet-stream", "video/mp4", "application/json", ""}
	for _, ct := range active {
		if !IsActiveContentType(ct) {
			t.Errorf("%q should be active", ct)
		}
	}
	for _, ct := range passive {
		if IsActiveContentType(ct) {
			t.Errorf("%q should be passive", ct)
		}
	}
}

func s3TestRouter(contentType string, useReader bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/:bucket/*key", S3ObjectResponseHeaders(), func(c *gin.Context) {
		body := "<html><script>alert(1)</script></html>"
		if useReader {
			c.DataFromReader(http.StatusOK, int64(len(body)), contentType, strings.NewReader(body), nil)
			return
		}
		c.Data(http.StatusOK, contentType, []byte(body))
	})
	r.HEAD("/:bucket/*key", S3ObjectResponseHeaders(), func(c *gin.Context) {
		c.Header("Content-Type", contentType)
		c.Status(http.StatusOK)
	})
	return r
}

func TestS3ObjectResponseHeadersForcesAttachmentForActiveTypes(t *testing.T) {
	for _, useReader := range []bool{false, true} {
		r := s3TestRouter("text/html", useReader)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/b/evil.html", nil))
		if got := w.Header().Get("Content-Disposition"); got != "attachment" {
			t.Errorf("reader=%v: Content-Disposition = %q, want attachment", useReader, got)
		}
		if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("reader=%v: X-Content-Type-Options = %q", useReader, got)
		}
	}

	// HEAD never writes a body; the headers must still be set.
	r := s3TestRouter("image/svg+xml", false)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodHead, "/b/x.svg", nil))
	if got := w.Header().Get("Content-Disposition"); got != "attachment" {
		t.Errorf("HEAD: Content-Disposition = %q, want attachment", got)
	}
}

func TestS3ObjectResponseHeadersLeavesPassiveTypesInline(t *testing.T) {
	r := s3TestRouter("image/png", false)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/b/pic.png", nil))
	if got := w.Header().Get("Content-Disposition"); got != "" {
		t.Errorf("passive type got Content-Disposition %q", got)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
}

func TestS3ObjectResponseHeadersRespectsSignedDispositionOverride(t *testing.T) {
	r := s3TestRouter("text/html", false)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/b/page.html?response-content-disposition=inline", nil))
	if got := w.Header().Get("Content-Disposition"); got == "attachment" {
		t.Error("explicit response-content-disposition should not be overridden")
	}
}

func TestConsoleSecurityHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ConsoleSecurityHeaders(SecurityHeadersConfig{HSTS: true, HSTSMaxAge: 600}))
	ok := func(c *gin.Context) { c.String(http.StatusOK, "ok") }
	r.GET("/api/buckets/:name/objects/*key", ok)
	r.GET("/api/docs/*any", ok)
	r.NoRoute(ok)

	get := func(host, path string) http.Header {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = host
		r.ServeHTTP(w, req)
		return w.Header()
	}

	h := get("bkt.example.com", "/buckets")
	if h.Get("Content-Security-Policy") != ConsoleCSP {
		t.Errorf("SPA CSP = %q", h.Get("Content-Security-Policy"))
	}
	if h.Get("X-Frame-Options") != "DENY" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") == "" {
		t.Errorf("missing hardening headers: %v", h)
	}
	if h.Get("Strict-Transport-Security") != "max-age=600" {
		t.Errorf("HSTS = %q", h.Get("Strict-Transport-Security"))
	}

	h = get("bkt.example.com", "/api/buckets/b/objects/evil.html")
	if !strings.HasPrefix(h.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("object download CSP = %q, want sandbox", h.Get("Content-Security-Policy"))
	}

	h = get("bkt.example.com", "/api/docs/index.html")
	if h.Get("Content-Security-Policy") != ConsoleCSP {
		t.Errorf("Swagger CSP = %q", h.Get("Content-Security-Policy"))
	}

	for _, host := range []string{"localhost:9443", "127.0.0.1:9443", "[::1]:9443", "app.localhost"} {
		if hsts := get(host, "/").Get("Strict-Transport-Security"); hsts != "" {
			t.Errorf("HSTS sent for %s: %q", host, hsts)
		}
	}
}
