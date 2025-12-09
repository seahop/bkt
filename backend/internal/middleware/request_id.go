package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// RequestIDMiddleware adds a unique request ID to each request
// This helps with debugging and tracing requests through logs
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Check if client provided X-Request-ID header (for request tracing)
		requestID := c.GetHeader("X-Request-ID")

		// Generate new UUID if not provided
		if requestID == "" {
			requestID = uuid.New().String()
		}

		// Store in context for use in handlers
		c.Set("request_id", requestID)

		// Add to response headers for client-side debugging
		c.Header("X-Request-ID", requestID)

		c.Next()
	}
}
