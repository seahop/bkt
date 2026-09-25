package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestBucketOrRoutesTrailingSlashToBucketHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var got string
	bucket := func(c *gin.Context) { got = "bucket:" + c.Param("bucket"); c.Status(http.StatusOK) }
	object := func(c *gin.Context) { got = "object:" + c.Param("key"); c.Status(http.StatusOK) }

	r := gin.New()
	r.PUT("/:bucket", bucket)
	r.PUT("/:bucket/*key", BucketOr(bucket, object))

	for path, want := range map[string]string{
		"/b":         "bucket:b",
		"/b/":        "bucket:b",
		"/b/k.txt":   "object:/k.txt",
		"/b/dir/":    "object:/dir/",
		"/b/a/b.txt": "object:/a/b.txt",
	} {
		got = ""
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, path, nil))
		if got != want {
			t.Errorf("PUT %s → %q, want %q (status %d)", path, got, want, w.Code)
		}
	}
}
