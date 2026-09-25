package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bkt/internal/metrics"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
)

func countSeries(c prometheus.Collector) int {
	ch := make(chan prometheus.Metric, 1024)
	go func() { c.Collect(ch); close(ch) }()
	n := 0
	for range ch {
		n++
	}
	return n
}

func TestMetricMethodBounded(t *testing.T) {
	for _, m := range []string{"GET", "HEAD", "PUT", "POST", "DELETE", "OPTIONS", "PATCH"} {
		if got := metricMethod(m); got != m {
			t.Errorf("metricMethod(%q) = %q", m, got)
		}
	}
	for _, m := range []string{"get", "PROPFIND", "TRACE", "CONNECT", strings.Repeat("X", 100000)} {
		if got := metricMethod(m); got != "OTHER" {
			t.Errorf("metricMethod(%.10q...) = %q, want OTHER", m, got)
		}
	}
}

func TestMetricsMiddlewareDoesNotCreateSeriesPerMethod(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(MetricsMiddleware())
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	before := countSeries(metrics.HTTPRequestsTotal)
	for i := 0; i < 50; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("M"+strings.Repeat("A", i), "/x", nil))
	}
	after := countSeries(metrics.HTTPRequestsTotal)
	if after-before > 2 {
		t.Fatalf("50 random methods created %d new series", after-before)
	}
}

func TestRejectUnknownMethods(t *testing.T) {
	h := RejectUnknownMethods(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	for m, want := range map[string]int{"GET": http.StatusTeapot, "PATCH": http.StatusTeapot, "PROPFIND": http.StatusNotImplemented, "TRACE": http.StatusNotImplemented} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(m, "/", nil))
		if w.Code != want {
			t.Errorf("%s: %d, want %d", m, w.Code, want)
		}
	}
}
