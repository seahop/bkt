package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"

	"bkt/internal/database"
	"bkt/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	// IdempotencyKeyHeader is the header name for idempotency keys
	IdempotencyKeyHeader = "Idempotency-Key"

	// IdempotencyKeyTTL is how long idempotency keys are valid (24 hours)
	IdempotencyKeyTTL = 24 * time.Hour
)

// responseWriter wraps gin.ResponseWriter to capture the response
type responseWriter struct {
	gin.ResponseWriter
	body       *bytes.Buffer
	statusCode int
}

func (w *responseWriter) Write(data []byte) (int, error) {
	w.body.Write(data)
	return w.ResponseWriter.Write(data)
}

func (w *responseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

// IdempotencyMiddleware handles idempotency key processing
func IdempotencyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Only apply to mutating requests (POST, PUT, PATCH, DELETE)
		if c.Request.Method != http.MethodPost &&
		   c.Request.Method != http.MethodPut &&
		   c.Request.Method != http.MethodPatch &&
		   c.Request.Method != http.MethodDelete {
			c.Next()
			return
		}

		// Check for idempotency key header
		idempotencyKey := c.GetHeader(IdempotencyKeyHeader)
		if idempotencyKey == "" {
			// No idempotency key provided, proceed normally
			c.Next()
			return
		}

		// Validate idempotency key format (should be UUID-like or at least 16 chars)
		if len(idempotencyKey) < 16 {
			c.JSON(http.StatusBadRequest, models.ErrorResponse{
				Error:   "Invalid idempotency key",
				Message: "Idempotency key must be at least 16 characters",
			})
			c.Abort()
			return
		}

		// Get user ID from context (set by auth middleware)
		userIDVal, exists := c.Get("user_id")
		if !exists {
			// No user authentication, skip idempotency (or require auth)
			c.Next()
			return
		}
		userID := userIDVal.(uuid.UUID)

		// Read request body to compute hash
		var requestBody []byte
		if c.Request.Body != nil {
			var err error
			requestBody, err = io.ReadAll(c.Request.Body)
			if err != nil {
				c.JSON(http.StatusInternalServerError, models.ErrorResponse{
					Error:   "Failed to read request body",
					Message: err.Error(),
				})
				c.Abort()
				return
			}
			// Restore request body for the handler
			c.Request.Body = io.NopCloser(bytes.NewReader(requestBody))
		}

		// Compute request hash (SHA256 of body)
		hash := sha256.Sum256(requestBody)
		requestHash := hex.EncodeToString(hash[:])

		// Check if idempotency key already exists
		var existingKey models.IdempotencyKey
		err := database.DB.Where("key = ? AND user_id = ?", idempotencyKey, userID).First(&existingKey).Error

		if err == nil {
			// Key exists, check if expired
			if time.Now().After(existingKey.ExpiresAt) {
				// Key expired, delete it and proceed with new request
				database.DB.Delete(&existingKey)
			} else {
				// Key still valid, verify request matches
				if existingKey.Method != c.Request.Method || existingKey.Path != c.Request.URL.Path {
					c.JSON(http.StatusConflict, models.ErrorResponse{
						Error:   "Idempotency key conflict",
						Message: fmt.Sprintf("Key already used for %s %s", existingKey.Method, existingKey.Path),
					})
					c.Abort()
					return
				}

				// Verify request body matches (prevent replay attacks with different body)
				if existingKey.RequestHash != requestHash {
					c.JSON(http.StatusUnprocessableEntity, models.ErrorResponse{
						Error:   "Request body mismatch",
						Message: "Request body differs from original request with same idempotency key",
					})
					c.Abort()
					return
				}

				// Return cached response
				c.Data(existingKey.StatusCode, "application/json", []byte(existingKey.ResponseBody))
				c.Abort()
				return
			}
		} else if err != gorm.ErrRecordNotFound {
			// Database error
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{
				Error:   "Idempotency check failed",
				Message: err.Error(),
			})
			c.Abort()
			return
		}

		// Key doesn't exist or expired, wrap response writer to capture response
		responseBodyBuffer := &bytes.Buffer{}
		writer := &responseWriter{
			ResponseWriter: c.Writer,
			body:           responseBodyBuffer,
			statusCode:     http.StatusOK, // default
		}
		c.Writer = writer

		// Process request
		c.Next()

		// After request processing, store idempotency key if request was successful
		// Only cache successful responses (2xx status codes)
		if writer.statusCode >= 200 && writer.statusCode < 300 {
			idempotencyRecord := models.IdempotencyKey{
				Key:          idempotencyKey,
				UserID:       userID,
				Method:       c.Request.Method,
				Path:         c.Request.URL.Path,
				StatusCode:   writer.statusCode,
				ResponseBody: responseBodyBuffer.String(),
				RequestHash:  requestHash,
				ExpiresAt:    time.Now().Add(IdempotencyKeyTTL),
			}

			// Save to database (best effort - don't fail request if storage fails)
			if err := database.DB.Create(&idempotencyRecord).Error; err != nil {
				// Debug logging (uncomment for troubleshooting)
				// fmt.Printf("Warning: Failed to store idempotency key: %v\n", err)
			}
		}
	}
}

// CleanupExpiredIdempotencyKeys removes expired idempotency keys from the database
// This should be called periodically (e.g., via a cron job or background goroutine)
func CleanupExpiredIdempotencyKeys() error {
	result := database.DB.Where("expires_at < ?", time.Now()).Delete(&models.IdempotencyKey{})
	return result.Error
}
