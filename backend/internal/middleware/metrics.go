package middleware

import (
	"bkt/internal/metrics"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// MetricsMiddleware records per-request HTTP metrics for Prometheus
func MetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		path := c.FullPath()
		if path == "" {
			path = "unmatched"
		}

		metrics.HTTPRequestsTotal.WithLabelValues(
			c.Request.Method,
			path,
			strconv.Itoa(c.Writer.Status()),
		).Inc()

		metrics.HTTPRequestDuration.WithLabelValues(
			c.Request.Method,
			path,
		).Observe(time.Since(start).Seconds())
	}
}
