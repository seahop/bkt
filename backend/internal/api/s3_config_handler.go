package api

import (
	"fmt"
	"net/http"
	"strings"

	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/security"
	"bkt/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type S3ConfigHandler struct {
	config       *config.Config
	auditService *services.AuditService
}

func NewS3ConfigHandler(cfg *config.Config) *S3ConfigHandler {
	return &S3ConfigHandler{config: cfg, auditService: services.NewAuditService()}
}

// ListS3Configs lists all S3 configurations (admin only)
// @Summary List S3 configurations
// @Description Admin-only. Returns all S3 backend configurations stored in the system.
// @Tags s3-configs
// @Accept json
// @Produce json
// @Success 200 {array} models.S3Configuration
// @Failure 403 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/s3-configs [get]
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
// @Summary Create an S3 configuration
// @Description Admin-only. Creates a new S3 backend configuration. Credentials are encrypted before storage. If marked as default, any existing default is unset.
// @Tags s3-configs
// @Accept json
// @Produce json
// @Param request body models.CreateS3ConfigRequest true "S3 configuration details"
// @Success 201 {object} models.S3Configuration
// @Failure 400 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 409 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/s3-configs [post]
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

	// Invalidate S3 config cache after creation
	InvalidateS3ConfigCache()

	{
		uid, uname := actor(c)
		_ = h.auditService.LogSuccess(c, uid, uname, "s3config.create", "s3_configuration", s3Config.ID.String(), s3Config.Name, nil)
	}
	c.JSON(http.StatusCreated, s3Config)
}

// GetS3Config gets a specific S3 configuration (admin only)
// @Summary Get an S3 configuration
// @Description Admin-only. Returns the details of a specific S3 backend configuration by ID.
// @Tags s3-configs
// @Accept json
// @Produce json
// @Param id path string true "S3 Configuration ID"
// @Success 200 {object} models.S3Configuration
// @Failure 400 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/s3-configs/{id} [get]
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
// @Summary Update an S3 configuration
// @Description Admin-only. Updates an existing S3 backend configuration. New credentials are encrypted before storage (credential rotation is always allowed). Changing endpoint, region, bucket_prefix, use_ssl or force_path_style is refused with 409 while any bucket uses the configuration, since it would re-route those buckets' data. bucket_prefix: omit to keep, "" to clear. The S3 config cache is invalidated on success.
// @Tags s3-configs
// @Accept json
// @Produce json
// @Param id path string true "S3 Configuration ID"
// @Param request body models.UpdateS3ConfigRequest true "Updated S3 configuration fields"
// @Success 200 {object} models.S3Configuration
// @Failure 400 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 409 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/s3-configs/{id} [put]
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

	// Settings that decide WHERE a bucket's data lives cannot change while
	// buckets are pinned to this configuration: every such bucket would
	// silently be served from a different location (and the listing
	// reconcile would then prune its metadata). Credential rotation, the
	// name and the default flag stay editable.
	if changed := s3ConfigLocationChanges(&s3Config, &req); len(changed) > 0 {
		names, err := bucketsUsingS3Config(configUUID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{
				Error:   "Failed to check S3 configuration usage",
				Message: err.Error(),
			})
			return
		}
		if len(names) > 0 {
			c.JSON(http.StatusConflict, models.ErrorResponse{
				Error: "S3 configuration is in use",
				Message: fmt.Sprintf("Cannot change %s while buckets use this configuration (it would re-route their data): %s",
					strings.Join(changed, ", "), strings.Join(names, ", ")),
			})
			return
		}
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
	if req.BucketPrefix != nil {
		s3Config.BucketPrefix = *req.BucketPrefix
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

	// Credentials: a value equal to the stored ciphertext is the console
	// echoing back what GET returned (access_key_id is served encrypted), not
	// a new key — encrypting it again would corrupt the credential. Unchanged
	// credentials are re-encrypted under the current key (the "re-save to
	// re-encrypt" path for credentials written under an older key).
	accessKeyID, secretAccessKey := req.AccessKeyID, req.SecretAccessKey
	if accessKeyID == s3Config.AccessKeyID {
		accessKeyID = ""
	}
	if secretAccessKey == s3Config.SecretAccessKey {
		secretAccessKey = ""
	}
	if accessKeyID == "" {
		if plain, derr := security.DecryptSecretKey(s3Config.AccessKeyID); derr == nil {
			accessKeyID = plain
		}
	}
	if secretAccessKey == "" {
		if plain, derr := security.DecryptSecretKey(s3Config.SecretAccessKey); derr == nil {
			secretAccessKey = plain
		}
	}
	if accessKeyID != "" {
		encryptedAccessKeyID, err := security.EncryptSecretKey(accessKeyID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{
				Error:   "Failed to encrypt access key ID",
				Message: err.Error(),
			})
			return
		}
		s3Config.AccessKeyID = encryptedAccessKeyID
	}
	if secretAccessKey != "" {
		// Encrypt secret access key before storing (CRITICAL security requirement)
		encryptedSecretAccessKey, err := security.EncryptSecretKey(secretAccessKey)
		if err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{
				Error:   "Failed to encrypt secret access key",
				Message: err.Error(),
			})
			return
		}
		s3Config.SecretAccessKey = encryptedSecretAccessKey
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

	// Invalidate S3 config cache after update
	InvalidateS3ConfigCache()

	{
		uid, uname := actor(c)
		_ = h.auditService.LogSuccess(c, uid, uname, "s3config.update", "s3_configuration", s3Config.ID.String(), s3Config.Name, nil)
	}
	c.JSON(http.StatusOK, s3Config)
}

// DeleteS3Config deletes an S3 configuration (admin only)
// @Summary Delete an S3 configuration
// @Description Admin-only. Deletes an S3 backend configuration. Fails if any buckets are currently using this configuration.
// @Tags s3-configs
// @Accept json
// @Produce json
// @Param id path string true "S3 Configuration ID"
// @Success 200 {object} models.SuccessResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 409 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/s3-configs/{id} [delete]
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

	// Check if any buckets are using this configuration. Every S3 bucket
	// served by a DB configuration has its id pinned (buckets without one use
	// the .env settings), so this covers all buckets whose data lives there.
	names, err := bucketsUsingS3Config(configUUID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to check S3 configuration usage",
			Message: err.Error(),
		})
		return
	}
	if len(names) > 0 {
		c.JSON(http.StatusConflict, models.ErrorResponse{
			Error:   "Cannot delete S3 configuration",
			Message: "Configuration is in use by buckets (delete those buckets first): " + strings.Join(names, ", "),
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

	// Invalidate S3 config cache after deletion
	InvalidateS3ConfigCache()

	{
		uid, uname := actor(c)
		_ = h.auditService.LogSuccess(c, uid, uname, "s3config.delete", "s3_configuration", s3Config.ID.String(), s3Config.Name, nil)
	}

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: "S3 configuration deleted successfully",
	})
}

// maxListedBuckets bounds the bucket names quoted in an "in use" error.
const maxListedBuckets = 20

// bucketsUsingS3Config returns (up to maxListedBuckets, then a "+N more"
// entry) the names of buckets pinned to the S3 configuration.
func bucketsUsingS3Config(configID uuid.UUID) ([]string, error) {
	var names []string
	if err := database.DB.Model(&models.Bucket{}).Where("s3_config_id = ?", configID).
		Order("name ASC").Limit(maxListedBuckets+1).Pluck("name", &names).Error; err != nil {
		return nil, err
	}
	if len(names) > maxListedBuckets {
		var total int64
		if err := database.DB.Model(&models.Bucket{}).Where("s3_config_id = ?", configID).Count(&total).Error; err != nil {
			return nil, err
		}
		names = append(names[:maxListedBuckets], fmt.Sprintf("(+%d more)", total-maxListedBuckets))
	}
	return names, nil
}

// s3ConfigLocationChanges lists the settings in req that would change where
// the configuration's buckets live (endpoint, region, bucket prefix, TLS and
// addressing style). Fields that are omitted or equal to the stored value are
// not changes, so a console form that re-submits every field only counts the
// fields the user actually edited.
func s3ConfigLocationChanges(cur *models.S3Configuration, req *models.UpdateS3ConfigRequest) []string {
	var changed []string
	if req.Endpoint != "" && req.Endpoint != cur.Endpoint {
		changed = append(changed, "endpoint")
	}
	if req.Region != "" && req.Region != cur.Region {
		changed = append(changed, "region")
	}
	if req.BucketPrefix != nil && *req.BucketPrefix != cur.BucketPrefix {
		changed = append(changed, "bucket_prefix")
	}
	if req.UseSSL != nil && *req.UseSSL != cur.UseSSL {
		changed = append(changed, "use_ssl")
	}
	if req.ForcePathStyle != nil && *req.ForcePathStyle != cur.ForcePathStyle {
		changed = append(changed, "force_path_style")
	}
	return changed
}
