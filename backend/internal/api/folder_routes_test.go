package api

import (
	"net/http"
	"testing"

	"bkt/internal/config"

	"github.com/gin-gonic/gin"
)

// The console routes register without gin wildcard/static conflicts, and the
// query-keyed object routes, folder and deleted-objects routes are mounted.
func TestConsoleRoutesRegister(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	r := gin.New()
	registerAPIRoutes(r, cfg)

	want := map[string]bool{
		http.MethodGet + " /api/buckets/:name/object":             false,
		http.MethodDelete + " /api/buckets/:name/object":          false,
		http.MethodHead + " /api/buckets/:name/object":            false,
		http.MethodGet + " /api/buckets/:name/objects/*key":       false,
		http.MethodGet + " /api/buckets/:name/folders":            false,
		http.MethodDelete + " /api/buckets/:name/folders":         false,
		http.MethodPost + " /api/buckets/:name/folders/move":      false,
		http.MethodGet + " /api/buckets/:name/deleted-objects":    false,
		http.MethodGet + " /api/buckets/:name/object-versions":    false,
		http.MethodDelete + " /api/buckets/:name/object-versions": false,
	}
	for _, rt := range r.Routes() {
		if _, ok := want[rt.Method+" "+rt.Path]; ok {
			want[rt.Method+" "+rt.Path] = true
		}
	}
	for route, found := range want {
		if !found {
			t.Errorf("route %s not registered", route)
		}
	}
}

func TestWithQueryObjectKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var got string
	r := gin.New()
	r.GET("/o", withQueryObjectKey(func(c *gin.Context) {
		got = c.Param("key")
		c.Status(http.StatusOK)
	}))
	for _, tc := range []struct{ query, key string }{
		{"key=..%2Fx", "/../x"},
		{"key=%2Fleading", "//leading"},
		{"key=.%252e%2Fh", "/.%2e/h"},
		{"key=a+b%2Bc", "/a b+c"},
	} {
		got = ""
		w := itDo(r, http.MethodGet, "/o?"+tc.query, nil)
		if w.Code != http.StatusOK || got != tc.key {
			t.Errorf("%s: %d key param %q, want %q", tc.query, w.Code, got, tc.key)
		}
	}
	if w := itDo(r, http.MethodGet, "/o", nil); w.Code != http.StatusBadRequest {
		t.Errorf("missing key: %d", w.Code)
	}
	if w := itDo(r, http.MethodGet, "/o?key=", nil); w.Code != http.StatusBadRequest {
		t.Errorf("empty key: %d", w.Code)
	}
}
