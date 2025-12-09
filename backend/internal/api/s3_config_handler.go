package api

import (
	"net/http"
	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/security"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type S3ConfigHandler struct {
	config *config.Config
}

func NewS3ConfigHandler(cfg *config.Config) *S3ConfigHandler {
	return &S3ConfigHandler{config: cfg}
}

// ListS3Configs lists all S3 configurations (admin only)
func (h *S3ConfigHandler) ListS3Configs(c *gin.Context) {
	isAdmin, _ := c.Get("is_admin")

	if !isAdmin.(bool) {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error: "Only administrators can list S3 configurations",
		})
		return
	}

	var configs []models.S3Configuration
	if err := database.DB.Find(&configs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to list S3 configurations",
			Message: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, configs)
}

// CreateS3Config creates a new S3 configuration (admin only)
func (h *S3ConfigHandler) CreateS3Config(c *gin.Context) {
	isAdmin, _ := c.Get("is_admin")

	if !isAdmin.(bool) {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error: "Only administrators can create S3 configurations",
		})
		return
	}

	var req models.CreateS3ConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Message: err.Error(),
		})
		return
	}

	// Check if config with same name already exists
	var existingConfig models.S3Configuration
	if err := database.DB.Where("name = ?", req.Name).First(&existingConfig).Error; err == nil {
		c.JSON(http.StatusConflict, models.ErrorResponse{
			Error: "S3 configuration with this name already exists",
		})
		return
	}

	// Set default values for booleans if not provided
	useSSL := true
	if req.UseSSL != nil {
		useSSL = *req.UseSSL
	}

	forcePathStyle := false
	if req.ForcePathStyle != nil {
		forcePathStyle = *req.ForcePathStyle
	}

	// Encrypt S3 credentials before storing (CRITICAL security requirement)
	encryptedAccessKeyID, err := security.EncryptSecretKey(req.AccessKeyID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to encrypt access key ID",
			Message: err.Error(),
		})
		return
	}

	encryptedSecretAccessKey, err := security.EncryptSecretKey(req.SecretAccessKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to encrypt secret access key",
			Message: err.Error(),
		})
		return
	}

	// Create S3 configuration with encrypted credentials
	s3Config := models.S3Configuration{
		Name:            req.Name,
		Endpoint:        req.Endpoint,
		Region:          req.Region,
		AccessKeyID:     encryptedAccessKeyID,     // Encrypted for database storage
		SecretAccessKey: encryptedSecretAccessKey, // Encrypted for database storage
		BucketPrefix:    req.BucketPrefix,
		UseSSL:          useSSL,
		ForcePathStyle:  forcePathStyle,
		IsDefault:       req.IsDefault,
	}

	// Use transaction to atomically unset existing default and create new config (prevents TOCTOU race)
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		// If this is set as default, unset any existing default within the transaction
		if req.IsDefault {
			if err := tx.Model(&models.S3Configuration{}).
				Where("is_default = ?", true).
				Update("is_default", false).Error; err != nil {
				return err
			}
		}

		// Create the new S3 configuration
		return tx.Create(&s3Config).Error
	})

	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to create S3 configuration",
			Message: err.Error(),
		})
		return
	}

	c.JSON(http.StatusCreated, s3Config)
}

// GetS3Config gets a specific S3 configuration (admin only)
func (h *S3ConfigHandler) GetS3Config(c *gin.Context) {
	isAdmin, _ := c.Get("is_admin")

	if !isAdmin.(bool) {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error: "Only administrators can view S3 configurations",
		})
		return
	}

	configID := c.Param("id")
	configUUID, err := uuid.Parse(configID)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Invalid S3 configuration ID",
		})
		return
	}

	var s3Config models.S3Configuration
	if err := database.DB.Where("id = ?", configUUID).First(&s3Config).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "S3 configuration not found",
		})
		return
	}

	c.JSON(http.StatusOK, s3Config)
}

// UpdateS3Config updates an S3 configuration (admin only)
func (h *S3ConfigHandler) UpdateS3Config(c *gin.Context) {
	isAdmin, _ := c.Get("is_admin")

	if !isAdmin.(bool) {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error: "Only administrators can update S3 configurations",
		})
		return
	}

	configID := c.Param("id")
	configUUID, err := uuid.Parse(configID)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Invalid S3 configuration ID",
		})
		return
	}

	var req models.UpdateS3ConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Message: err.Error(),
		})
		return
	}

	var s3Config models.S3Configuration
	if err := database.DB.Where("id = ?", configUUID).First(&s3Config).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "S3 configuration not found",
		})
		return
	}

	// Update fields if provided
	if req.Name != "" {
		s3Config.Name = req.Name
	}
	if req.Endpoint != "" {
		s3Config.Endpoint = req.Endpoint
	}
	if req.Region != "" {
		s3Config.Region = req.Region
	}
	if req.AccessKeyID != "" {
		// Encrypt access key ID before storing
		encryptedAccessKeyID, err := security.EncryptSecretKey(req.AccessKeyID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{
				Error:   "Failed to encrypt access key ID",
				Message: err.Error(),
			})
			return
		}
		s3Config.AccessKeyID = encryptedAccessKeyID
	}
	if req.SecretAccessKey != "" {
		// Encrypt secret access key before storing (CRITICAL security requirement)
		encryptedSecretAccessKey, err := security.EncryptSecretKey(req.SecretAccessKey)
		if err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{
				Error:   "Failed to encrypt secret access key",
				Message: err.Error(),
			})
			return
		}
		s3Config.SecretAccessKey = encryptedSecretAccessKey
	}
	if req.BucketPrefix != "" {
		s3Config.BucketPrefix = req.BucketPrefix
	}
	if req.UseSSL != nil {
		s3Config.UseSSL = *req.UseSSL
	}
	if req.ForcePathStyle != nil {
		s3Config.ForcePathStyle = *req.ForcePathStyle
	}
	if req.IsDefault != nil {
		s3Config.IsDefault = *req.IsDefault
	}

	// Use transaction to atomically unset existing default and save config (prevents TOCTOU race)
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		// If setting as default, unset any existing default within the transaction
		if req.IsDefault != nil && *req.IsDefault {
			if err := tx.Model(&models.S3Configuration{}).
				Where("is_default = ? AND id != ?", true, configUUID).
				Update("is_default", false).Error; err != nil {
				return err
			}
		}

		// Save the updated S3 configuration
		return tx.Save(&s3Config).Error
	})

	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to update S3 configuration",
			Message: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, s3Config)
}

// DeleteS3Config deletes an S3 configuration (admin only)
func (h *S3ConfigHandler) DeleteS3Config(c *gin.Context) {
	isAdmin, _ := c.Get("is_admin")

	if !isAdmin.(bool) {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error: "Only administrators can delete S3 configurations",
		})
		return
	}

	configID := c.Param("id")
	configUUID, err := uuid.Parse(configID)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Invalid S3 configuration ID",
		})
		return
	}

	var s3Config models.S3Configuration
	if err := database.DB.Where("id = ?", configUUID).First(&s3Config).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "S3 configuration not found",
		})
		return
	}

	// Check if any buckets are using this configuration
	var bucketCount int64
	database.DB.Model(&models.Bucket{}).Where("s3_config_id = ?", configUUID).Count(&bucketCount)
	if bucketCount > 0 {
		c.JSON(http.StatusConflict, models.ErrorResponse{
			Error:   "Cannot delete S3 configuration",
			Message: "Configuration is in use by one or more buckets. Please update or delete those buckets first.",
		})
		return
	}

	if err := database.DB.Delete(&s3Config).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to delete S3 configuration",
			Message: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: "S3 configuration deleted successfully",
	})
}
