package middleware

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestIdempotencyNeverCachesSecrets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if !cacheableResponse(c, []byte(`{"id":"b1","name":"bucket"}`)) {
		t.Error("ordinary response should be cacheable")
	}
	for _, body := range []string{
		`{"access_key":"AK","secret_key":"SK"}`,
		`{"token":"t","refresh_token":"r"}`,
	} {
		if cacheableResponse(c, []byte(body)) {
			t.Errorf("response with secret material cached: %s", body)
		}
	}
	c.Set(CtxNoIdempotencyStore, true)
	if cacheableResponse(c, []byte(`{"ok":true}`)) {
		t.Error("handler opt-out must be honoured")
	}
}
