package middleware

import (
	"bkt/internal/metrics"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// knownMethods is the fixed set of HTTP methods the API serves. Anything else
// is reported to Prometheus as "OTHER" and rejected by RejectUnknownMethods.
var knownMethods = map[string]bool{
	http.MethodGet:     true,
	http.MethodHead:    true,
	http.MethodPut:     true,
	http.MethodPost:    true,
	http.MethodDelete:  true,
	http.MethodOptions: true,
	http.MethodPatch:   true,
}

// metricMethod maps a request method onto a bounded label set. The raw method
// is attacker-controlled (any token up to the header size limit), so using it
// directly as a Prometheus label would let unauthenticated clients create an
// unbounded number of time series.
func metricMethod(m string) string {
	if knownMethods[m] {
		return m
	}
	return "OTHER"
}

// RejectUnknownMethods answers 501 Not Implemented for any method outside the
// fixed set the API serves, before routing or any other processing.
func RejectUnknownMethods(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !knownMethods[r.Method] {
			http.Error(w, "method not implemented", http.StatusNotImplemented)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// MetricsMiddleware records per-request HTTP metrics for Prometheus. All
// labels are bounded: method via metricMethod, path is the route template (or
// "unmatched"), status is a 3-digit code.
func MetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		path := c.FullPath()
		if path == "" {
			path = "unmatched"
		}
		method := metricMethod(c.Request.Method)

		metrics.HTTPRequestsTotal.WithLabelValues(
			method,
			path,
			strconv.Itoa(c.Writer.Status()),
		).Inc()

		metrics.HTTPRequestDuration.WithLabelValues(
			method,
			path,
		).Observe(time.Since(start).Seconds())
	}
}
