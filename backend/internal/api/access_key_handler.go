package api

import (
	"fmt"
	"net/http"
	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/security"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type AccessKeyHandler struct {
	config *config.Config
}

func NewAccessKeyHandler(cfg *config.Config) *AccessKeyHandler {
	return &AccessKeyHandler{config: cfg}
}

// GenerateAccessKey generates a new access key and secret key pair for the authenticated user
func (h *AccessKeyHandler) GenerateAccessKey(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error: "Unauthorized",
		})
		return
	}

	// Check if user already has maximum number of access keys (limit to 5 per user for security)
	var count int64
	database.DB.Model(&models.AccessKey{}).Where("user_id = ? AND is_active = ?", userID, true).Count(&count)
	if count >= 5 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Maximum access keys reached",
			Message: "You can have a maximum of 5 active access keys. Please revoke an existing key first.",
		})
		return
	}

	// Generate cryptographically secure access key and secret key
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

	// Create access key record
	newAccessKey := models.AccessKey{
		UserID:             userID.(uuid.UUID),
		AccessKey:          accessKey,
		SecretKeyHash:      secretKeyHash,
		SecretKeyEncrypted: secretKeyEncrypted,
		IsActive:           true,
	}

	if err := database.DB.Create(&newAccessKey).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to create access key",
			Message: err.Error(),
		})
		return
	}

	// Return the secret key ONLY ONCE - it will never be shown again
	c.JSON(http.StatusCreated, gin.H{
		"message":     "Access key created successfully",
		"access_key":  accessKey,
		"secret_key":  secretKey, // ONLY TIME this is ever returned
		"created_at":  newAccessKey.CreatedAt,
		"warning":     "Save your secret key now. It will not be shown again!",
	})
}

// ListAccessKeys lists all access keys for the authenticated user
func (h *AccessKeyHandler) ListAccessKeys(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error: "Unauthorized",
		})
		return
	}

	accessKeys := make([]models.AccessKey, 0)
	if err := database.DB.Where("user_id = ?", userID).Order("created_at DESC").Find(&accessKeys).Error; err != nil {
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

	// Update last used timestamp
	now := time.Now()
	key.LastUsedAt = &now
	database.DB.Save(&key)

	return &key.User, nil
}

// GetAccessKeyStats returns statistics about access keys for the user
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
