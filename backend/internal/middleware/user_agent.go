package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// UserAgentValidationMiddleware validates User-Agent headers to prevent malformed requests
func UserAgentValidationMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		userAgent := c.GetHeader("User-Agent")

		// Allow empty User-Agent (some legitimate clients don't send it)
		if userAgent == "" {
			c.Next()
			return
		}

		// Basic validation: check length and for suspicious patterns
		// Max length: 512 characters (typical user agents are < 200 chars)
		if len(userAgent) > 512 {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "Invalid User-Agent",
				"message": "User-Agent header too long",
			})
			return
		}

		// Check for null bytes (indicates potential injection attempt)
		if strings.Contains(userAgent, "\x00") {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "Invalid User-Agent",
				"message": "User-Agent contains invalid characters",
			})
			return
		}

		// Check for control characters (except tab and newline, which are sometimes present)
		for _, r := range userAgent {
			if r < 32 && r != 9 && r != 10 && r != 13 {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
					"error":   "Invalid User-Agent",
					"message": "User-Agent contains control characters",
				})
				return
			}
		}

		// Check for suspicious patterns that indicate malformed/malicious requests
		suspiciousPatterns := []string{
			"<script",  // XSS attempt
			"javascript:", // XSS attempt
			"${",       // Template injection
			"../../",   // Path traversal attempt
			"DROP TABLE", // SQL injection
			"UNION SELECT", // SQL injection
		}

		userAgentLower := strings.ToLower(userAgent)
		for _, pattern := range suspiciousPatterns {
			if strings.Contains(userAgentLower, strings.ToLower(pattern)) {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
					"error":   "Invalid User-Agent",
					"message": "User-Agent contains suspicious patterns",
				})
				return
			}
		}

		c.Next()
	}
}
