package middleware

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// http.NewResponseController must reach the real connection through our
// gin.ResponseWriter wrappers (STORAGE handlers extend read deadlines for
// large uploads).
func TestResponseWrappersSupportResponseController(t *testing.T) {
	gin.SetMode(gin.TestMode)
	results := make(chan map[string]error, 1)
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		out := map[string]error{}
		base := c.Writer
		wrappers := map[string]http.ResponseWriter{
			"gin":         base,
			"idempotency": &responseWriter{ResponseWriter: base, body: &bytes.Buffer{}},
			"disposition": &dispositionWriter{ResponseWriter: base},
		}
		for name, w := range wrappers {
			out[name] = http.NewResponseController(w).SetReadDeadline(time.Now().Add(time.Minute))
		}
		results <- out
		c.Status(http.StatusNoContent)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	srv := &http.Server{Handler: r}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	resp, err := http.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for name, err := range <-results {
		if errors.Is(err, http.ErrNotSupported) {
			t.Errorf("%s wrapper: SetReadDeadline not supported (missing Unwrap)", name)
		} else if err != nil {
			t.Errorf("%s wrapper: %v", name, err)
		}
	}
}
