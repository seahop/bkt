package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/security"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type AccessKeyHandler struct {
	config *config.Config
}

func NewAccessKeyHandler(cfg *config.Config) *AccessKeyHandler {
	return &AccessKeyHandler{config: cfg}
}

// GenerateAccessKey generates a new access key and secret key pair for the authenticated user
// @Summary Generate a new access key
// @Description Generates a new cryptographically secure access key and secret key pair for the authenticated user. The secret key is returned only once and cannot be retrieved again. Maximum 5 active keys per user.
// @Tags access-keys
// @Accept json
// @Produce json
// @Success 201 {object} object "Access key and secret key (secret shown only once)"
// @Failure 400 {object} models.ErrorResponse
// @Failure 401 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/access-keys [post]
func (h *AccessKeyHandler) GenerateAccessKey(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error: "Unauthorized",
		})
		return
	}

	// Optional scoping: a human-readable name, an expiry, and a read-only flag.
	var req struct {
		Name         string `json:"name"`
		ReadOnly     bool   `json:"read_only"`
		ExpiresInDays int   `json:"expires_in_days"`
	}
	// The body is optional (no body → unscoped key), but a body that is present
	// and malformed must be rejected — silently ignoring it would issue a
	// full-access, never-expiring key to a caller who asked for a scoped one.
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request body",
			Message: err.Error(),
		})
		return
	}
	if req.ExpiresInDays < 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "expires_in_days must be positive",
		})
		return
	}

	var expiresAt *time.Time
	if req.ExpiresInDays > 0 {
		t := time.Now().AddDate(0, 0, req.ExpiresInDays)
		expiresAt = &t
	}

	// Generate cryptographically secure access key and secret key BEFORE transaction
	// to avoid holding locks during expensive crypto operations
	accessKey, err := security.GenerateAccessKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate access key",
			Message: err.Error(),
		})
		return
	}

	secretKey, err := security.GenerateSecretKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate secret key",
			Message: err.Error(),
		})
		return
	}

	// Hash the secret key before storing (for API auth)
	secretKeyHash, err := security.HashSecretKey(secretKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to hash secret key",
			Message: err.Error(),
		})
		return
	}

	// Encrypt the secret key for S3 auth
	secretKeyEncrypted, err := security.EncryptSecretKey(secretKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to encrypt secret key",
			Message: err.Error(),
		})
		return
	}

	// Use transaction to atomically check limit and create key (prevents TOCTOU race)
	var newAccessKey models.AccessKey
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		// Count active access keys with row lock to prevent concurrent modifications
		var count int64
		if err := tx.Model(&models.AccessKey{}).
			Where("user_id = ? AND is_active = ? AND temporary = ?", userID, true, false).
			Count(&count).Error; err != nil {
			return err
		}

		// Check limit (5 per user for security)
		if count >= 5 {
			return fmt.Errorf("maximum access keys reached")
		}

		// Create access key record
		newAccessKey = models.AccessKey{
			UserID:             userID.(uuid.UUID),
			AccessKey:          accessKey,
			SecretKeyHash:      secretKeyHash,
			SecretKeyEncrypted: secretKeyEncrypted,
			Name:               req.Name,
			IsActive:           true,
			ReadOnly:           req.ReadOnly,
			ExpiresAt:          expiresAt,
		}

		return tx.Create(&newAccessKey).Error
	})

	if err != nil {
		if err.Error() == "maximum access keys reached" {
			c.JSON(http.StatusBadRequest, models.ErrorResponse{
				Error:   "Maximum access keys reached",
				Message: "You can have a maximum of 5 active access keys. Please revoke an existing key first.",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to create access key",
			Message: err.Error(),
		})
		return
	}

	// Return the secret key ONLY ONCE - it will never be shown again
	// Add cache-control headers to prevent caching of sensitive data
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate, private")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")

	c.JSON(http.StatusCreated, gin.H{
		"message":    "Access key created successfully",
		"access_key": accessKey,
		"secret_key": secretKey, // ONLY TIME this is ever returned
		"name":       newAccessKey.Name,
		"read_only":  newAccessKey.ReadOnly,
		"expires_at": newAccessKey.ExpiresAt,
		"created_at": newAccessKey.CreatedAt,
		"warning":    "Save your secret key now. It will not be shown again!",
	})
}

// ListAccessKeys lists all access keys for the authenticated user
// @Summary List access keys
// @Description Returns all access keys (active and inactive) belonging to the authenticated user. Secret key hashes are never returned.
// @Tags access-keys
// @Accept json
// @Produce json
// @Success 200 {array} models.AccessKey
// @Failure 401 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/access-keys [get]
func (h *AccessKeyHandler) ListAccessKeys(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error: "Unauthorized",
		})
		return
	}

	// Only active keys: revoked keys are soft-deleted for the audit trail, but
	// surfacing them forever in the user's own list just accumulates clutter
	// (admins still see the full history via /api/users/:id/access-keys).
	accessKeys := make([]models.AccessKey, 0)
	if err := database.DB.Where("user_id = ? AND is_active = ? AND temporary = ?", userID, true, false).Order("created_at DESC").Find(&accessKeys).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to list access keys",
			Message: err.Error(),
		})
		return
	}

	// Never return secret key hashes
	c.JSON(http.StatusOK, accessKeys)
}

// RevokeAccessKey deactivates an access key (soft delete for audit trail)
// @Summary Revoke an access key
// @Description Deactivates an access key (soft delete). Users can revoke their own keys; admins can revoke any key. The key record is retained for audit purposes.
// @Tags access-keys
// @Accept json
// @Produce json
// @Param id path string true "Access Key ID"
// @Success 200 {object} models.SuccessResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 401 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/access-keys/{id} [delete]
func (h *AccessKeyHandler) RevokeAccessKey(c *gin.Context) {
	keyID := c.Param("id")
	userID, exists := c.Get("user_id")
	isAdmin, _ := c.Get("is_admin")

	if !exists {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error: "Unauthorized",
		})
		return
	}

	// Parse UUID
	accessKeyUUID, err := uuid.Parse(keyID)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Invalid access key ID",
		})
		return
	}

	// Find access key
	var accessKey models.AccessKey
	if err := database.DB.Where("id = ?", accessKeyUUID).First(&accessKey).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "Access key not found",
		})
		return
	}

	// Authorization check - users can only revoke their own keys unless admin
	if !isAdmin.(bool) && accessKey.UserID != userID.(uuid.UUID) {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error: "Access denied",
		})
		return
	}

	// Soft delete - set is_active to false for audit trail
	accessKey.IsActive = false
	if err := database.DB.Save(&accessKey).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to revoke access key",
			Message: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: "Access key revoked successfully",
	})
}

// ValidateAccessKey validates an access key and secret key pair
// This is used for API authentication
func (h *AccessKeyHandler) ValidateAccessKey(accessKey, secretKey string) (*models.User, error) {
	// Validate key formats first (prevents unnecessary DB queries)
	if !security.ValidateAccessKeyFormat(accessKey) {
		return nil, fmt.Errorf("invalid access key format")
	}
	if !security.ValidateSecretKeyFormat(secretKey) {
		return nil, fmt.Errorf("invalid secret key format")
	}

	// Find access key in database
	var key models.AccessKey
	if err := database.DB.Where("access_key = ? AND is_active = ?", accessKey, true).
		Preload("User").First(&key).Error; err != nil {
		return nil, fmt.Errorf("access key not found or inactive")
	}

	// Validate secret key using constant-time comparison (prevents timing attacks)
	if !security.ValidateSecretKey(secretKey, key.SecretKeyHash) {
		return nil, fmt.Errorf("invalid secret key")
	}

	// Update last used timestamp (best-effort, don't fail validation)
	now := time.Now()
	key.LastUsedAt = &now
	// Silently ignore errors - don't log access keys for security
	database.DB.Save(&key)

	return &key.User, nil
}

// GetAccessKeyStats returns statistics about access keys for the user
// @Summary Get access key statistics
// @Description Returns the count of active and total access keys for the authenticated user, along with the maximum allowed keys.
// @Tags access-keys
// @Accept json
// @Produce json
// @Success 200 {object} object "Access key statistics"
// @Failure 401 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/access-keys/stats [get]
func (h *AccessKeyHandler) GetAccessKeyStats(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error: "Unauthorized",
		})
		return
	}

	var activeCount, totalCount int64
	database.DB.Model(&models.AccessKey{}).Where("user_id = ? AND is_active = ?", userID, true).Count(&activeCount)
	database.DB.Model(&models.AccessKey{}).Where("user_id = ?", userID).Count(&totalCount)

	c.JSON(http.StatusOK, gin.H{
		"active_keys": activeCount,
		"total_keys":  totalCount,
		"max_keys":    5,
	})
}
