package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestPublicReadQueryAllowed(t *testing.T) {
	cases := map[string]bool{
		"":                                 true,
		"response-content-type=text/plain": true,
		"response-content-disposition=attachment&response-cache-control=no-store&response-expires=x&response-content-language=en&response-content-encoding=gzip": true,
		"versionId=null":                     false,
		"acl":                                false,
		"tagging":                            false,
		"uploadId=1":                         false,
		"partNumber=1":                       false,
		"attributes":                         false,
		"X-Amz-Signature=x":                  false,
		"Response-Content-Type=text/html":    false, // parameter names are case-sensitive
		"response-content-type=a;b":          false, // does not parse cleanly
		"response-content-type=x&versions":   false,
		"response-content-type=x&uploads":    false,
		"response-content-type=x&lifecycle=": false,
	}
	for q, want := range cases {
		if got := PublicReadQueryAllowed(q); got != want {
			t.Errorf("PublicReadQueryAllowed(%q) = %v, want %v", q, got, want)
		}
	}
}

// publicReadCandidate must agree with the S3 router's routing: only
// GET/HEAD on /:bucket/*key with a non-empty key.
func TestPublicReadCandidate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var got bool
	probe := func(c *gin.Context) { got = publicReadCandidate(c); c.Status(http.StatusNoContent) }
	r := gin.New()
	g := r.Group("")
	g.Use(probe)
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPost, http.MethodDelete} {
		g.Handle(m, "/", func(*gin.Context) {})
		g.Handle(m, "/:bucket", func(*gin.Context) {})
		g.Handle(m, "/:bucket/*key", func(*gin.Context) {})
	}
	cases := []struct {
		method, target string
		want           bool
	}{
		{http.MethodGet, "/b/k", true},
		{http.MethodHead, "/b/k", true},
		{http.MethodGet, "/b/dir/k.txt?response-content-type=text/plain", true},
		{http.MethodGet, "/b/k?versionId=1", false},
		{http.MethodGet, "/b/", false},
		{http.MethodHead, "/b/", false},
		{http.MethodGet, "/b", false},
		{http.MethodGet, "/", false},
		{http.MethodPut, "/b/k", false},
		{http.MethodPost, "/b/k", false},
		{http.MethodDelete, "/b/k", false},
	}
	for _, c := range cases {
		got = false
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(c.method, c.target, nil))
		if got != c.want {
			t.Errorf("%s %s: candidate=%v, want %v", c.method, c.target, got, c.want)
		}
	}
}

func TestAbortSlowDown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rl := NewRateLimiter(30, time.Minute) // 30/min → one token per 2s
	r := gin.New()
	r.GET("/", func(c *gin.Context) { abortSlowDown(c, rl) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "2" ||
		w.Body.String() != `<Error><Code>SlowDown</Code><Message>Please reduce your request rate.</Message></Error>` {
		t.Errorf("SlowDown: %d Retry-After=%q %s", w.Code, w.Header().Get("Retry-After"), w.Body.String())
	}
}
