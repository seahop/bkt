package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/logger"
	"bkt/internal/models"
	"bkt/internal/security"
	"bkt/internal/services"
	"bkt/internal/storage"
	"bkt/internal/validation"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// s3ConfigCacheEntry represents a cached S3 configuration with expiration
type s3ConfigCacheEntry struct {
	Config    *s3ConfigData
	ExpiresAt time.Time
}

// s3ConfigData holds decrypted S3 configuration data for caching
type s3ConfigData struct {
	Endpoint        string
	Region          string
	AccessKeyID     string // Decrypted
	SecretAccessKey string // Decrypted
	BucketPrefix    string
	UseSSL          bool
	ForcePathStyle  bool
}

// Global S3 config cache with 5 minute TTL (reduces database load)
var (
	s3ConfigCache   = make(map[string]*s3ConfigCacheEntry)
	s3ConfigCacheMu sync.RWMutex
	s3ConfigCacheTTL = 5 * time.Minute
)

type BucketHandler struct {
	config        *config.Config
	policyService *services.PolicyService
	auditService  *services.AuditService
}

func NewBucketHandler(cfg *config.Config) *BucketHandler {
	return &BucketHandler{
		config:        cfg,
		policyService: services.NewPolicyService(),
		auditService:  services.NewAuditService(),
	}
}

// getS3ConfigFromCache retrieves S3 config from cache if valid
func getS3ConfigFromCache(cacheKey string) (*s3ConfigData, bool) {
	s3ConfigCacheMu.RLock()
	defer s3ConfigCacheMu.RUnlock()

	entry, exists := s3ConfigCache[cacheKey]
	if !exists {
		return nil, false
	}

	// Check if entry has expired
	if time.Now().After(entry.ExpiresAt) {
		return nil, false
	}

	return entry.Config, true
}

// setS3ConfigInCache stores S3 config in cache with TTL
func setS3ConfigInCache(cacheKey string, config *s3ConfigData) {
	s3ConfigCacheMu.Lock()
	defer s3ConfigCacheMu.Unlock()

	s3ConfigCache[cacheKey] = &s3ConfigCacheEntry{
		Config:    config,
		ExpiresAt: time.Now().Add(s3ConfigCacheTTL),
	}
}

// InvalidateS3ConfigCache invalidates cached S3 configurations (called when configs are modified)
func InvalidateS3ConfigCache() {
	s3ConfigCacheMu.Lock()
	defer s3ConfigCacheMu.Unlock()

	// Clear entire cache when any config is modified
	s3ConfigCache = make(map[string]*s3ConfigCacheEntry)
}

// getStorageBackend creates a storage backend instance based on the bucket's configuration
// Hybrid approach: If bucket has s3_config_id, use that; otherwise use .env config
func (h *BucketHandler) getStorageBackend(bucket *models.Bucket) (storage.StorageBackend, error) {
	backend := bucket.StorageBackend
	if backend == "" {
		backend = "local" // Default to local
	}

	// If not S3, return local storage
	if backend != "s3" {
		return storage.NewLocalStorage(h.config.Storage.RootPath), nil
	}

	// S3 backend: Load configuration with caching (reduces database load)
	var endpoint, region, accessKeyID, secretAccessKey, bucketPrefix string
	var useSSL, forcePathStyle bool

	// Determine cache key and load config
	var cacheKey string
	var configData *s3ConfigData
	var cacheHit bool

	if bucket.S3ConfigID != nil {
		// Bucket-specific S3 configuration
		cacheKey = bucket.S3ConfigID.String()
		configData, cacheHit = getS3ConfigFromCache(cacheKey)

		if !cacheHit {
			// Cache miss - load from database
			var s3Config models.S3Configuration
			if err := database.DB.Where("id = ?", bucket.S3ConfigID).First(&s3Config).Error; err == nil {
				// Decrypt S3 credentials (they're stored encrypted for security)
				decryptedAccessKeyID, err := security.DecryptSecretKey(s3Config.AccessKeyID)
				if err != nil {
					return nil, fmt.Errorf("failed to decrypt access key ID: %w", err)
				}
				decryptedSecretAccessKey, err := security.DecryptSecretKey(s3Config.SecretAccessKey)
				if err != nil {
					return nil, fmt.Errorf("failed to decrypt secret access key: %w", err)
				}

				// Create config data and cache it
				configData = &s3ConfigData{
					Endpoint:        s3Config.Endpoint,
					Region:          s3Config.Region,
					AccessKeyID:     decryptedAccessKeyID,
					SecretAccessKey: decryptedSecretAccessKey,
					BucketPrefix:    s3Config.BucketPrefix,
					UseSSL:          s3Config.UseSSL,
					ForcePathStyle:  s3Config.ForcePathStyle,
				}
				setS3ConfigInCache(cacheKey, configData)
			} else {
				// Config not found - fall back to .env (don't cache fallback)
				configData = &s3ConfigData{
					Endpoint:        h.config.Storage.S3.Endpoint,
					Region:          h.config.Storage.S3.Region,
					AccessKeyID:     h.config.Storage.S3.AccessKeyID,
					SecretAccessKey: h.config.Storage.S3.SecretAccessKey,
					BucketPrefix:    h.config.Storage.S3.BucketPrefix,
					UseSSL:          h.config.Storage.S3.UseSSL,
					ForcePathStyle:  h.config.Storage.S3.ForcePathStyle,
				}
			}
		}
	} else {
		// No specific config - use default S3 configuration
		cacheKey = "default"
		configData, cacheHit = getS3ConfigFromCache(cacheKey)

		if !cacheHit {
			// Cache miss - load default from database
			var defaultConfig models.S3Configuration
			if err := database.DB.Where("is_default = ?", true).First(&defaultConfig).Error; err == nil {
				// Decrypt S3 credentials (they're stored encrypted for security)
				decryptedAccessKeyID, err := security.DecryptSecretKey(defaultConfig.AccessKeyID)
				if err != nil {
					return nil, fmt.Errorf("failed to decrypt default access key ID: %w", err)
				}
				decryptedSecretAccessKey, err := security.DecryptSecretKey(defaultConfig.SecretAccessKey)
				if err != nil {
					return nil, fmt.Errorf("failed to decrypt default secret access key: %w", err)
				}

				// Create config data and cache it
				configData = &s3ConfigData{
					Endpoint:        defaultConfig.Endpoint,
					Region:          defaultConfig.Region,
					AccessKeyID:     decryptedAccessKeyID,
					SecretAccessKey: decryptedSecretAccessKey,
					BucketPrefix:    defaultConfig.BucketPrefix,
					UseSSL:          defaultConfig.UseSSL,
					ForcePathStyle:  defaultConfig.ForcePathStyle,
				}
				setS3ConfigInCache(cacheKey, configData)
			} else {
				// No default config - fall back to .env (don't cache fallback)
				configData = &s3ConfigData{
					Endpoint:        h.config.Storage.S3.Endpoint,
					Region:          h.config.Storage.S3.Region,
					AccessKeyID:     h.config.Storage.S3.AccessKeyID,
					SecretAccessKey: h.config.Storage.S3.SecretAccessKey,
					BucketPrefix:    h.config.Storage.S3.BucketPrefix,
					UseSSL:          h.config.Storage.S3.UseSSL,
					ForcePathStyle:  h.config.Storage.S3.ForcePathStyle,
				}
			}
		}
	}

	// Extract values from config data
	endpoint = configData.Endpoint
	region = configData.Region
	accessKeyID = configData.AccessKeyID
	secretAccessKey = configData.SecretAccessKey
	bucketPrefix = configData.BucketPrefix
	useSSL = configData.UseSSL
	forcePathStyle = configData.ForcePathStyle

	storageBackend, err := storage.NewStorageBackend(
		backend,
		h.config.Storage.RootPath,
		endpoint,
		region,
		accessKeyID,
		secretAccessKey,
		bucketPrefix,
		useSSL,
		forcePathStyle,
		h.config.Storage.S3SSE,
	)
	if err != nil {
		// Log configuration error - don't silently fallback as this can hide issues
		logger.Warn("Failed to initialize storage backend", map[string]interface{}{
			"backend": backend,
			"bucket":  bucket.Name,
			"error":   err.Error(),
		})

		// Return error if S3 was explicitly configured for this bucket
		// Silent fallback can lead to data being written to wrong storage
		if backend == "s3" {
			return nil, fmt.Errorf("S3 storage backend configuration error: %w", err)
		}

		// Only fallback to local if backend was "local" or unspecified
		logger.Info("Falling back to local storage", map[string]interface{}{
			"bucket": bucket.Name,
		})
		return storage.NewLocalStorage(h.config.Storage.RootPath), nil
	}

	return storageBackend, nil
}

// CreateBucket creates a new bucket
// @Summary Create a bucket
// @Description Admin-only. Creates a new storage bucket. If the bucket already exists in the storage backend, it is linked rather than re-created.
// @Tags buckets
// @Accept json
// @Produce json
// @Param request body models.CreateBucketRequest true "Bucket configuration"
// @Success 201 {object} models.Bucket
// @Failure 400 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 409 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/buckets [post]
func (h *BucketHandler) CreateBucket(c *gin.Context) {
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	var req models.CreateBucketRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Message: err.Error(),
		})
		return
	}

	// Validate bucket name according to S3 naming rules
	if err := validation.ValidateBucketName(req.Name); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid bucket name",
			Message: err.Error(),
		})
		return
	}

	// Validate region format
	if err := validation.ValidateRegion(req.Region); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid region",
			Message: err.Error(),
		})
		return
	}

	// Check policy permissions
	allowed, err := h.policyService.CheckBucketAccess(userUUID, req.Name, services.ActionCreateBucket)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Policy check failed",
			Message: err.Error(),
		})
		return
	}
	if !allowed {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Permission denied",
			Message: "You don't have permission to create this bucket",
		})
		return
	}

	// Check if bucket already exists in our database
	var existing models.Bucket
	if err := database.DB.Where("name = ?", req.Name).First(&existing).Error; err == nil {
		c.JSON(http.StatusConflict, models.ErrorResponse{
			Error: "Bucket already exists in this system",
		})
		return
	}

	// Create bucket struct (for storage backend check)
	bucket := models.Bucket{
		Name:           req.Name,
		OwnerID:        userUUID,
		IsPublic:       req.IsPublic,
		Region:         req.Region,
		StorageBackend: req.StorageBackend,
	}

	// Set S3 config ID if provided
	if req.S3ConfigID != nil && *req.S3ConfigID != "" {
		configUUID, err := uuid.Parse(*req.S3ConfigID)
		if err == nil {
			// Verify the S3 config exists
			var s3Config models.S3Configuration
			if err := database.DB.Where("id = ?", configUUID).First(&s3Config).Error; err == nil {
				bucket.S3ConfigID = &configUUID
			}
		}
	}

	if bucket.Region == "" {
		bucket.Region = "us-east-1"
	}

	// Default to local storage if not specified or invalid
	if bucket.StorageBackend != "local" && bucket.StorageBackend != "s3" {
		bucket.StorageBackend = "local"
	}

	// Check if bucket already exists in storage backend (S3 or local)
	// If it exists and we can access it, we'll "link" to it instead of creating a new one
	var linkedToExisting bool
	storageBackend, err := h.getStorageBackend(&bucket)
	if err == nil {
		exists, checkErr := storageBackend.BucketExists(bucket.Name)
		if checkErr != nil {
			// Permission issue - bucket might exist but we can't access it
			c.JSON(http.StatusForbidden, models.ErrorResponse{
				Error:   "Cannot access bucket in storage backend",
				Message: checkErr.Error(),
			})
			return
		}
		linkedToExisting = exists
	}

	// Create bucket record in database
	if err := database.DB.Create(&bucket).Error; err != nil {
		// Get user info for audit log
		username, _ := c.Get("username")

		// Log failure
		h.auditService.LogFailure(
			c,
			userUUID,
			username.(string),
			"CreateBucket",
			"Bucket",
			"",
			req.Name,
			err.Error(),
			map[string]interface{}{
				"bucket_name":     req.Name,
				"region":          req.Region,
				"storage_backend": req.StorageBackend,
				"is_public":       req.IsPublic,
			},
		)

		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to create bucket",
			Message: "An internal error occurred. Please try again.",
		})
		return
	}

	// If bucket doesn't exist in storage backend, create it
	if !linkedToExisting && storageBackend != nil {
		if err := storageBackend.CreateBucket(bucket.Name, bucket.Region); err != nil {
			logger.Warn("Failed to create bucket in storage backend", map[string]interface{}{
				"bucket_name":     bucket.Name,
				"storage_backend": bucket.StorageBackend,
				"error":           err.Error(),
			})
			// Don't fail the request - the database record was created
			// The bucket will be created lazily on first object upload if this fails
		} else {
			logger.Info("Bucket created in storage backend", map[string]interface{}{
				"bucket_name":     bucket.Name,
				"storage_backend": bucket.StorageBackend,
				"region":          bucket.Region,
			})
		}
	} else if linkedToExisting {
		logger.Info("Bucket linked to existing storage backend bucket", map[string]interface{}{
			"bucket_name":     bucket.Name,
			"storage_backend": bucket.StorageBackend,
		})
	}

	// Get user info for audit log
	username, _ := c.Get("username")

	// Log success
	h.auditService.LogSuccess(
		c,
		userUUID,
		username.(string),
		"CreateBucket",
		"Bucket",
		bucket.ID.String(),
		bucket.Name,
		map[string]interface{}{
			"bucket_name":       bucket.Name,
			"region":            bucket.Region,
			"storage_backend":   bucket.StorageBackend,
			"is_public":         bucket.IsPublic,
			"linked_to_existing": linkedToExisting,
		},
	)

	// Return response with indication of whether bucket was linked or created
	response := gin.H{
		"id":              bucket.ID,
		"name":            bucket.Name,
		"owner_id":        bucket.OwnerID,
		"is_public":       bucket.IsPublic,
		"region":          bucket.Region,
		"storage_backend": bucket.StorageBackend,
		"created_at":      bucket.CreatedAt,
		"updated_at":      bucket.UpdatedAt,
	}

	if linkedToExisting {
		response["message"] = "Bucket linked to existing storage. Any existing contents will be accessible."
		response["linked"] = true
	}

	c.JSON(http.StatusCreated, response)
}

// ListBuckets lists accessible buckets for the authenticated user
// @Summary List buckets
// @Description Returns all buckets the authenticated user has access to. Admins see all buckets; regular users see only buckets permitted by their attached policies.
// @Tags buckets
// @Accept json
// @Produce json
// @Success 200 {array} models.Bucket
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/buckets [get]
func (h *BucketHandler) ListBuckets(c *gin.Context) {
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)
	isAdmin, _ := c.Get("is_admin")

	var allBuckets []models.Bucket
	query := database.DB.Preload("Owner")

	// Fetch all buckets for filtering
	if err := query.Find(&allBuckets).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to fetch buckets",
			Message: err.Error(),
		})
		return
	}

	// Admin bypass - return all buckets
	if isAdmin.(bool) {
		c.JSON(http.StatusOK, allBuckets)
		return
	}

	// Use batch permission check to avoid N+1 queries (fixes CRITICAL performance issue)
	// Check if user has ANY of these common actions on each bucket
	actions := []string{
		services.ActionListBucket,
		services.ActionGetObject,
		services.ActionPutObject,
		services.ActionDeleteObject,
	}

	// Track buckets with access (use map to avoid duplicates)
	accessibleBucketMap := make(map[uuid.UUID]models.Bucket)

	// For each action, perform batch check and collect accessible buckets
	for _, action := range actions {
		bucketsWithAccess, err := h.policyService.FilterAccessibleBuckets(userUUID, allBuckets, action)
		if err != nil {
			// Log error but continue with other actions
			continue
		}
		// Add accessible buckets to map (deduplicates automatically)
		for _, bucket := range bucketsWithAccess {
			accessibleBucketMap[bucket.ID] = bucket
		}
	}

	// Convert map back to slice
	accessibleBuckets := make([]models.Bucket, 0, len(accessibleBucketMap))
	for _, bucket := range accessibleBucketMap {
		accessibleBuckets = append(accessibleBuckets, bucket)
	}

	c.JSON(http.StatusOK, accessibleBuckets)
}

// GetBucket returns details of a specific bucket
// @Summary Get a bucket
// @Description Returns the details of a specific bucket by name. Requires GetBucketLocation permission.
// @Tags buckets
// @Accept json
// @Produce json
// @Param name path string true "Bucket name"
// @Success 200 {object} models.Bucket
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/buckets/{name} [get]
func (h *BucketHandler) GetBucket(c *gin.Context) {
	bucketName := c.Param("name")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	var bucket models.Bucket
	if err := database.DB.Preload("Owner").Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "Bucket not found",
		})
		return
	}

	// Check policy permissions
	allowed, err := h.policyService.CheckBucketAccess(userUUID, bucketName, services.ActionGetBucketLocation)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Policy check failed",
			Message: err.Error(),
		})
		return
	}
	if !allowed {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Permission denied",
			Message: "You don't have permission to access this bucket",
		})
		return
	}

	c.JSON(http.StatusOK, bucket)
}

// DeleteBucket deletes a bucket and all its contents
// @Summary Delete a bucket
// @Description Admin-only. Deletes a bucket and all its objects from both the database and the storage backend.
// @Tags buckets
// @Accept json
// @Produce json
// @Param name path string true "Bucket name"
// @Success 200 {object} models.SuccessResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/buckets/{name} [delete]
func (h *BucketHandler) DeleteBucket(c *gin.Context) {
	bucketName := c.Param("name")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)
	username, _ := c.Get("username")

	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "Bucket not found",
		})
		return
	}

	// Check policy permissions
	allowed, err := h.policyService.CheckBucketAccess(userUUID, bucketName, services.ActionDeleteBucket)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Policy check failed",
			Message: err.Error(),
		})
		return
	}
	if !allowed {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Permission denied",
			Message: "You don't have permission to delete this bucket",
		})
		return
	}

	// Get storage backend for this bucket
	storageBackend, err := h.getStorageBackend(&bucket)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to get storage backend",
			Message: err.Error(),
		})
		return
	}

	// Get all objects in the bucket
	// WORM: a bucket with retention still covering any object or version
	// cannot be deleted (deleting the bucket would destroy retained data).
	if bucket.RetentionDays > 0 {
		cutoff := time.Now().AddDate(0, 0, -bucket.RetentionDays)
		var retained int64
		database.DB.Model(&models.Object{}).Where("bucket_id = ? AND updated_at > ?", bucket.ID, cutoff).Count(&retained)
		var retainedVers int64
		database.DB.Model(&models.ObjectVersion{}).Where("bucket_id = ? AND is_delete_marker = false AND content_modified_at > ?", bucket.ID, cutoff).Count(&retainedVers)
		if retained+retainedVers > 0 {
			c.JSON(http.StatusConflict, models.ErrorResponse{
				Error:   "Bucket under retention",
				Message: fmt.Sprintf("%d object(s)/version(s) are still within the %d-day retention period", retained+retainedVers, bucket.RetentionDays),
			})
			return
		}
	}

	var objects []models.Object
	if err := database.DB.Where("bucket_id = ?", bucket.ID).Find(&objects).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to list bucket objects",
			Message: err.Error(),
		})
		return
	}

	// Delete all objects from storage first
	var storageErrors []string
	for _, obj := range objects {
		if err := storageBackend.DeleteObject(bucketName, obj.Key); err != nil {
			// Log error but continue - we'll still try to delete the rest
			storageErrors = append(storageErrors, fmt.Sprintf("%s: %v", obj.Key, err))
		}
	}

	// Delete the bucket from storage backend (after objects are removed)
	if err := storageBackend.DeleteBucket(bucketName); err != nil {
		storageErrors = append(storageErrors, fmt.Sprintf("bucket deletion: %v", err))
	}

	// Use transaction to delete all objects and the bucket from database
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		// Delete all objects from database
		if len(objects) > 0 {
			if err := tx.Where("bucket_id = ?", bucket.ID).Delete(&models.ObjectVersion{}).Error; err != nil {
			return err
		}
		if err := tx.Where("bucket_id = ?", bucket.ID).Delete(&models.Object{}).Error; err != nil {
				return fmt.Errorf("failed to delete objects: %w", err)
			}
		}

		// Delete any bucket policies
		if err := tx.Where("bucket_id = ?", bucket.ID).Delete(&models.BucketPolicy{}).Error; err != nil {
			return fmt.Errorf("failed to delete bucket policies: %w", err)
		}

		// Delete the bucket
		if err := tx.Delete(&bucket).Error; err != nil {
			return fmt.Errorf("failed to delete bucket: %w", err)
		}

		return nil
	})

	if err != nil {
		// Log failure
		h.auditService.LogFailure(
			c,
			userUUID,
			username.(string),
			"DeleteBucket",
			"Bucket",
			bucket.ID.String(),
			bucket.Name,
			err.Error(),
			map[string]interface{}{
				"bucket_name":    bucket.Name,
				"owner_id":       bucket.OwnerID.String(),
				"objects_count":  len(objects),
				"storage_errors": storageErrors,
			},
		)

		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to delete bucket",
			Message: err.Error(),
		})
		return
	}

	// Log success
	h.auditService.LogSuccess(
		c,
		userUUID,
		username.(string),
		"DeleteBucket",
		"Bucket",
		bucket.ID.String(),
		bucket.Name,
		map[string]interface{}{
			"bucket_name":     bucket.Name,
			"owner_id":        bucket.OwnerID.String(),
			"objects_deleted": len(objects),
		},
	)

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: fmt.Sprintf("Bucket deleted successfully (%d objects removed)", len(objects)),
	})
}

// SetBucketPolicy sets an access policy on a bucket
// @Summary Set bucket policy
// @Description Admin-only. Sets an S3-style access policy document on the specified bucket. Requires PutBucketPolicy permission.
// @Tags buckets
// @Accept json
// @Produce json
// @Param name path string true "Bucket name"
// @Param request body object true "Bucket policy document" SchemaExample({"policy":"{\"Version\":\"2012-10-17\",...}"})
// @Success 200 {object} models.SuccessResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/buckets/{name}/policy [put]
func (h *BucketHandler) SetBucketPolicy(c *gin.Context) {
	bucketName := c.Param("name")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	// Check policy permissions - must have PutBucketPolicy permission
	allowed, err := h.policyService.CheckBucketAccess(userUUID, bucketName, services.ActionPutBucketPolicy)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Policy check failed",
			Message: err.Error(),
		})
		return
	}
	if !allowed {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Permission denied",
			Message: "You don't have permission to set bucket policy",
		})
		return
	}

	var req struct {
		Policy string `json:"policy" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Message: err.Error(),
		})
		return
	}

	// Set bucket policy using the service
	if err := h.policyService.SetBucketPolicy(bucketName, req.Policy); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Failed to set bucket policy",
			Message: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: "Bucket policy set successfully",
	})
}

// GetBucketPolicy retrieves the access policy for a bucket
// @Summary Get bucket policy
// @Description Returns the S3-style access policy document for the specified bucket. Requires GetBucketPolicy permission.
// @Tags buckets
// @Accept json
// @Produce json
// @Param name path string true "Bucket name"
// @Success 200 {object} object "Bucket policy document"
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/buckets/{name}/policy [get]
func (h *BucketHandler) GetBucketPolicy(c *gin.Context) {
	bucketName := c.Param("name")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	// Check policy permissions - must have GetBucketPolicy permission
	allowed, err := h.policyService.CheckBucketAccess(userUUID, bucketName, services.ActionGetBucketPolicy)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Policy check failed",
			Message: err.Error(),
		})
		return
	}
	if !allowed {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Permission denied",
			Message: "You don't have permission to get bucket policy",
		})
		return
	}

	// Get bucket policy using the service
	bucketPolicy, err := h.policyService.GetBucketPolicy(bucketName)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error:   "Bucket policy not found",
			Message: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"policy": bucketPolicy.PolicyDocument,
	})
}

// ListObjects lists objects in a bucket
// @Summary List objects in a bucket
// @Description Returns objects stored in the specified bucket. Supports prefix filtering and pagination. For S3-backed buckets, automatically syncs new and removed objects from the storage backend.
// @Tags buckets
// @Accept json
// @Produce json
// @Param name path string true "Bucket name"
// @Param prefix query string false "Filter objects by key prefix"
// @Param max-keys query string false "Maximum number of objects to return (default 1000, max 1000)"
// @Success 200 {object} object "List of objects with count"
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/buckets/{name}/objects [get]
func (h *BucketHandler) ListObjects(c *gin.Context) {
	bucketName := c.Param("name")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	// Get bucket from database
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "Bucket not found",
		})
		return
	}

	// Check policy permissions
	allowed, err := h.policyService.CheckBucketAccess(userUUID, bucketName, services.ActionListBucket)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Policy check failed",
			Message: err.Error(),
		})
		return
	}
	if !allowed {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Permission denied",
			Message: "You don't have permission to list objects in this bucket",
		})
		return
	}

	// Query parameters for pagination and filtering
	prefix := c.DefaultQuery("prefix", "")
	maxKeys := 1000
	if mk := c.Query("max-keys"); mk != "" {
		if parsed, err := strconv.Atoi(mk); err == nil && parsed > 0 && parsed <= 1000 {
			maxKeys = parsed
		}
	}
	// Keyset pagination: the continuation token is the last key of the previous
	// page; the next page is everything strictly after it in key order.
	continuationToken := c.Query("continuation-token")

	// Get objects from database
	query := database.DB.Where("bucket_id = ?", bucket.ID)
	if prefix != "" {
		// Escape LIKE wildcards to prevent SQL injection via prefix parameter
		escapedPrefix := validation.EscapeLikeWildcards(prefix)
		query = query.Where("key LIKE ?", escapedPrefix+"%")
	}
	if continuationToken != "" {
		query = query.Where("key > ?", continuationToken)
	}

	// Fetch one extra row to learn whether more pages exist.
	var objects []models.Object
	if err := query.Limit(maxKeys + 1).Order("key ASC").Find(&objects).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to list objects",
			Message: err.Error(),
		})
		return
	}
	dbTruncated := len(objects) > maxKeys
	if dbTruncated {
		objects = objects[:maxKeys]
	}
	// Continuation basis if reconciliation below prunes the whole page.
	lastExaminedKey := ""
	if len(objects) > 0 {
		lastExaminedKey = objects[len(objects)-1].Key
	}

	// Sync with actual storage backend (S3 or local)
	// This handles both:
	// 1. Removing stale DB entries (objects deleted directly from S3)
	// 2. Adding new entries (objects in S3 that aren't in DB, e.g., from linked buckets)
	if bucket.StorageBackend == "s3" {
		storageBackend, err := h.getStorageBackend(&bucket)
		if err == nil {
			// Get actual objects from S3
			s3Objects, err := storageBackend.ListObjects(bucketName, prefix)
			if err == nil {
				// Build maps for comparison
				s3KeysMap := make(map[string]storage.ObjectInfo)
				for _, obj := range s3Objects {
					s3KeysMap[obj.Key] = obj
				}

				dbKeysMap := make(map[string]bool)
				for _, obj := range objects {
					dbKeysMap[obj.Key] = true
				}

				// Find objects in S3 but not in database (need to add)
				// Limit sync to prevent overwhelming DB on huge buckets
				// Users can paginate/navigate to sync more incrementally
				const maxSyncPerRequest = 1000
				newObjects := make([]models.Object, 0)
				for key, s3Obj := range s3KeysMap {
					if !dbKeysMap[key] {
						// Parse LastModified time
						lastModified := time.Now()
						if s3Obj.LastModified != "" {
							if parsed, err := time.Parse(time.RFC3339, s3Obj.LastModified); err == nil {
								lastModified = parsed
							}
						}

						newObjects = append(newObjects, models.Object{
							BucketID:    bucket.ID,
							Key:         key,
							Size:        s3Obj.Size,
							ContentType: s3Obj.ContentType,
							ETag:        s3Obj.ETag,
							StoragePath: key,
							CreatedAt:   lastModified,
							UpdatedAt:   lastModified,
						})

						// Cap sync to avoid memory/DB overload on massive buckets
						if len(newObjects) >= maxSyncPerRequest {
							break
						}
					}
				}

				// Add new objects to database in background using batch inserts
				// Batch size of 100 balances memory usage vs query count
				const batchSize = 100
				if len(newObjects) > 0 {
					go func(objs []models.Object) {
						// Process in batches to avoid huge queries
						for i := 0; i < len(objs); i += batchSize {
							end := i + batchSize
							if end > len(objs) {
								end = len(objs)
							}
							batch := objs[i:end]

							// Build batch insert query
							valueStrings := make([]string, 0, len(batch))
							valueArgs := make([]interface{}, 0, len(batch)*8)
							for _, obj := range batch {
								valueStrings = append(valueStrings, "(gen_random_uuid(), ?, ?, ?, ?, ?, ?, '', ?, ?)")
								valueArgs = append(valueArgs, obj.BucketID, obj.Key, obj.Size, obj.ContentType, obj.ETag, obj.StoragePath, obj.CreatedAt, obj.UpdatedAt)
							}

							query := fmt.Sprintf(`
								INSERT INTO objects (id, bucket_id, key, size, content_type, e_tag, storage_path, sha256, created_at, updated_at)
								VALUES %s
								ON CONFLICT (bucket_id, key) DO NOTHING
							`, strings.Join(valueStrings, ","))

							database.DB.Exec(query, valueArgs...)
						}
					}(newObjects)

					// Add to response immediately (don't wait for DB), but only
					// keys inside this page's range — keys at or before the
					// continuation token belong to earlier pages and would
					// duplicate entries the client already has.
					for _, obj := range newObjects {
						if continuationToken == "" || obj.Key > continuationToken {
							objects = append(objects, obj)
						}
					}
				}

				// Find objects in database but not in S3 (candidates for removal).
				// CRITICAL: only treat "absent from the S3 listing" as "deleted
				// from S3" when the listing is known COMPLETE. The backend caps
				// its listing at 10,000 objects; if we hit that cap the listing
				// is truncated and an absent key may simply be beyond the window,
				// not deleted. Deleting on a truncated (or, before the s3.go fix,
				// error-masked-as-empty) listing would wipe valid metadata.
				listingComplete := len(s3Objects) < storage.MaxListObjects

				validObjects := make([]models.Object, 0, len(objects))
				staleIDs := make([]uuid.UUID, 0)
				for _, obj := range objects {
					if _, exists := s3KeysMap[obj.Key]; exists {
						validObjects = append(validObjects, obj)
					} else if listingComplete && obj.ID != uuid.Nil {
						// Only prune when the listing is complete and this is a
						// persisted row (not one we just appended above).
						staleIDs = append(staleIDs, obj.ID)
					} else {
						// Listing incomplete: keep the row rather than risk
						// deleting valid metadata.
						validObjects = append(validObjects, obj)
					}
				}

				// Delete stale records from database in background (batched)
				if len(staleIDs) > 0 {
					go func(ids []uuid.UUID) {
						// Delete in batches of 100 to avoid huge IN clauses
						for i := 0; i < len(ids); i += batchSize {
							end := i + batchSize
							if end > len(ids) {
								end = len(ids)
							}
							database.DB.Where("id IN ?", ids[i:end]).Delete(&models.Object{})
						}
					}(staleIDs)
				}

				objects = validObjects
			}
		}
	}

	// Re-establish page invariants after the reconcile above may have added
	// (S3 sync) or removed (stale prune) entries: sorted by key, at most
	// maxKeys rows, with a token pointing just past the last row we cover.
	sort.Slice(objects, func(i, j int) bool { return objects[i].Key < objects[j].Key })
	isTruncated := dbTruncated
	if len(objects) > maxKeys {
		objects = objects[:maxKeys]
		isTruncated = true
	}
	nextToken := ""
	if isTruncated {
		if len(objects) > 0 {
			nextToken = objects[len(objects)-1].Key
		} else {
			// The whole page was pruned as stale but more DB rows exist beyond
			// it — continue from the last key this page examined.
			nextToken = lastExaminedKey
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"bucket":                  bucketName,
		"objects":                 objects,
		"count":                   len(objects),
		"is_truncated":            isTruncated,
		"next_continuation_token": nextToken,
	})
}

// UploadObject uploads an object to a bucket
// @Summary Upload an object
// @Description Uploads a file to the specified bucket. The content type is detected from file magic bytes. Requires PutObject permission.
// @Tags buckets
// @Accept multipart/form-data
// @Produce json
// @Param name path string true "Bucket name"
// @Param key formData string true "Object key (path within bucket)"
// @Param file formData file true "File to upload"
// @Success 200 {object} object "Upload result with ETag and content type"
// @Failure 400 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 413 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/buckets/{name}/objects [post]
func (h *BucketHandler) UploadObject(c *gin.Context) {
	bucketName := c.Param("name")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	// Get bucket from database
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "Bucket not found",
		})
		return
	}

	// Get object key from form or query
	objectKey := c.PostForm("key")
	if objectKey == "" {
		objectKey = c.Query("key")
	}
	if objectKey == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Object key is required",
		})
		return
	}

	// Validate object key to prevent path traversal and other attacks
	if err := validation.ValidateObjectKey(objectKey); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid object key",
			Message: err.Error(),
		})
		return
	}

	// Check policy permissions
	allowed, err := h.policyService.CheckObjectAccess(userUUID, bucketName, objectKey, services.ActionPutObject)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Policy check failed",
			Message: err.Error(),
		})
		return
	}
	if !allowed {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Permission denied",
			Message: "You don't have permission to upload objects to this bucket",
		})
		return
	}

	// Get uploaded file
	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Failed to get file",
			Message: err.Error(),
		})
		return
	}

	// Validate file size (prevent edge cases and resource abuse)
	if fileHeader.Size < 0 {
		// Negative size is invalid (should never happen, but check for safety)
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid file size",
			Message: "File size cannot be negative",
		})
		return
	}

	if fileHeader.Size > h.config.Storage.MaxFileSize {
		c.JSON(http.StatusRequestEntityTooLarge, models.ErrorResponse{
			Error:   "File too large",
			Message: fmt.Sprintf("Maximum file size is %d bytes", h.config.Storage.MaxFileSize),
		})
		return
	}

	// Warn about suspiciously large files even if under limit (potential resource abuse)
	// 1GB threshold for warning (could indicate accidental large file upload)
	if fileHeader.Size > 1*1024*1024*1024 {
		// Log warning but allow upload (admin may want to review)
		logger.Warn("Large file upload detected", map[string]interface{}{
			"object_key": objectKey,
			"size_bytes": fileHeader.Size,
			"size_mb":    fileHeader.Size / (1024 * 1024),
		})
	}

	// Open uploaded file
	file, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to open file",
			Message: err.Error(),
		})
		return
	}
	defer file.Close()

	// Detect actual content type from file magic numbers (don't trust client)
	detectedType, firstBytes, err := validation.DetectContentType(file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to detect content type",
			Message: err.Error(),
		})
		return
	}

	// Validate content type is safe
	if !validation.IsSafeContentType(detectedType) {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Forbidden file type",
			Message: fmt.Sprintf("File type '%s' is not allowed", detectedType),
		})
		return
	}

	// Use detected content type (from magic numbers, not from client header)
	contentType := detectedType

	// Create MultiReader to prepend the first bytes back to the stream
	combinedReader := io.MultiReader(bytes.NewReader(firstBytes), file)

	// Get storage backend for this bucket
	storageBackend, err := h.getStorageBackend(&bucket)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to initialize storage backend",
			Message: err.Error(),
		})
		return
	}

	// Save object using storage backend with timeout (prevents indefinite blocking on large uploads)
	// Use 10 minute timeout for uploads (configurable based on max file size)
	uploadTimeout := 10 * time.Minute
	ctx, cancel := context.WithTimeout(c.Request.Context(), uploadTimeout)
	defer cancel()

	// Run upload in goroutine to support timeout
	type uploadResult struct {
		err error
	}
	resultChan := make(chan uploadResult, 1)

	if qerr := checkBucketQuota(&bucket, fileHeader.Size); qerr != nil {
		c.JSON(http.StatusForbidden, models.ErrorResponse{Error: "Quota exceeded", Message: qerr.Error()})
		return
	}

	go func() {
		archivedVID, verr := prepareVersionedWrite(storageBackend, &bucket, objectKey)
		if verr != nil {
			resultChan <- uploadResult{err: verr}
			return
		}
		err := storageBackend.PutObject(bucketName, objectKey, combinedReader, fileHeader.Size, contentType, nil)
		if err != nil {
			rollbackVersionedWrite(storageBackend, &bucket, objectKey, archivedVID)
		}
		resultChan <- uploadResult{err: err}
	}()

	// Wait for upload or timeout
	select {
	case result := <-resultChan:
		if result.err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{
				Error:   "Failed to save object",
				Message: result.err.Error(),
			})
			return
		}
	case <-ctx.Done():
		c.JSON(http.StatusRequestTimeout, models.ErrorResponse{
			Error:   "Upload timeout",
			Message: fmt.Sprintf("Upload exceeded timeout of %v", uploadTimeout),
		})
		return
	}

	// Get object info (including ETag) from storage
	objectInfo, err := storageBackend.GetObjectInfo(bucketName, objectKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to get object info",
			Message: err.Error(),
		})
		return
	}

	// Use UPSERT to create or update object metadata in single query (performance optimization)
	now := time.Now()
	newVersionID := ""
	if bucket.Versioning == models.VersioningEnabled {
		newVersionID = uuid.New().String()
	}
	object := models.Object{
		BucketID:    bucket.ID,
		Key:         objectKey,
		Size:        objectInfo.Size,
		ContentType: objectInfo.ContentType,
		ETag:        objectInfo.ETag,
		StoragePath: objectKey,
		SHA256:      "",
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// PostgreSQL UPSERT: INSERT with ON CONFLICT UPDATE
	// This reduces 2 queries (SELECT + INSERT/UPDATE) to 1 query
	err = database.DB.Exec(`
		INSERT INTO objects (id, bucket_id, key, size, content_type, e_tag, storage_path, sha256, version_id, created_at, updated_at)
		VALUES (gen_random_uuid(), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (bucket_id, key)
		DO UPDATE SET
			size = EXCLUDED.size,
			content_type = EXCLUDED.content_type,
			e_tag = EXCLUDED.e_tag,
			storage_path = EXCLUDED.storage_path,
			sha256 = EXCLUDED.sha256,
			version_id = EXCLUDED.version_id,
			updated_at = EXCLUDED.updated_at
	`, object.BucketID, object.Key, object.Size, object.ContentType, object.ETag,
		object.StoragePath, object.SHA256, newVersionID, object.CreatedAt, object.UpdatedAt).Error

	if err != nil {
		// Clean up file if database operation fails
		storageBackend.DeleteObject(bucketName, objectKey)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to save object metadata",
			Message: err.Error(),
		})
		return
	}

	// Retrieve the object to get the ID and timestamps for response
	if err := database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, objectKey).First(&object).Error; err != nil {
		// Object was created but couldn't retrieve - log but don't fail the upload
		// The file is successfully stored, just return success without full details
	}

	notifyObjectEvent(&bucket, services.EventObjectCreated, objectKey, objectInfo.Size, objectInfo.ETag, newVersionID)

	c.JSON(http.StatusOK, gin.H{
		"message":      "Object uploaded successfully",
		"bucket":       bucketName,
		"key":          objectKey,
		"size":         objectInfo.Size,
		"etag":         objectInfo.ETag,
		"content_type": objectInfo.ContentType,
	})
}

// DownloadObject downloads an object from a bucket
// @Summary Download an object
// @Description Streams the content of an object from the specified bucket. Add ?download=true to force a download attachment. Requires GetObject permission.
// @Tags buckets
// @Produce application/octet-stream
// @Param name path string true "Bucket name"
// @Param key path string true "Object key"
// @Param download query string false "Set to 'true' to force download attachment"
// @Success 200 {file} binary "Object content"
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/buckets/{name}/objects/{key} [get]
func (h *BucketHandler) DownloadObject(c *gin.Context) {
	bucketName := c.Param("name")
	objectKey := strings.TrimPrefix(c.Param("key"), "/")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	// Get bucket from database
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "Bucket not found",
		})
		return
	}

	// Check policy permissions
	allowed, err := h.policyService.CheckObjectAccess(userUUID, bucketName, objectKey, services.ActionGetObject)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Policy check failed",
			Message: err.Error(),
		})
		return
	}
	if !allowed {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Permission denied",
			Message: "You don't have permission to download this object",
		})
		return
	}

	// Get object metadata from database
	var object models.Object
	if err := database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, objectKey).First(&object).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "Object not found",
		})
		return
	}

	// Get storage backend for this bucket
	storageBackend, err := h.getStorageBackend(&bucket)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to initialize storage backend",
			Message: err.Error(),
		})
		return
	}

	// Get object from storage backend
	file, err := storageBackend.GetObject(bucketName, objectKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to retrieve object",
			Message: err.Error(),
		})
		return
	}
	defer file.Close()

	// Set response headers
	c.Header("Content-Type", object.ContentType)
	c.Header("ETag", fmt.Sprintf("\"%s\"", object.ETag))
	c.Header("Last-Modified", object.UpdatedAt.UTC().Format(http.TimeFormat))
	c.Header("Accept-Ranges", "bytes")

	// Set content disposition based on query parameter
	if c.Query("download") == "true" {
		filename := filepath.Base(objectKey)
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	} else {
		c.Header("Content-Disposition", "inline")
	}

	// Honor Range requests so browsers can seek media and resume downloads.
	// We advertise Accept-Ranges, so a 200-with-full-body response to a range
	// request corrupts the client's reassembled file.
	if start, length, ok, satisfiable := parseRange(c.GetHeader("Range"), object.Size); ok {
		if !satisfiable {
			c.Header("Content-Range", fmt.Sprintf("bytes */%d", object.Size))
			c.Status(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if seeker, isSeeker := file.(io.Seeker); isSeeker {
			if _, err := seeker.Seek(start, io.SeekStart); err != nil {
				c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to seek object", Message: err.Error()})
				return
			}
		} else if _, err := io.CopyN(io.Discard, file, start); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to read object range", Message: err.Error()})
			return
		}
		c.Header("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, object.Size))
		c.DataFromReader(http.StatusPartialContent, length, object.ContentType, io.LimitReader(file, length), nil)
		return
	}

	// Stream file to response — cap at object.Size so the body never exceeds the
	// declared Content-Length (multipart-assembled files may have trailing bytes).
	c.Header("Content-Length", strconv.FormatInt(object.Size, 10))
	c.DataFromReader(http.StatusOK, object.Size, object.ContentType, io.LimitReader(file, object.Size), nil)
}

// DeleteObject deletes an object from a bucket
// @Summary Delete an object
// @Description Deletes an object and its metadata from the specified bucket. Requires DeleteObject permission.
// @Tags buckets
// @Produce json
// @Param name path string true "Bucket name"
// @Param key path string true "Object key"
// @Success 200 {object} models.SuccessResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/buckets/{name}/objects/{key} [delete]
func (h *BucketHandler) DeleteObject(c *gin.Context) {
	bucketName := c.Param("name")
	objectKey := strings.TrimPrefix(c.Param("key"), "/")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	// Get bucket from database
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "Bucket not found",
		})
		return
	}

	// Check policy permissions
	allowed, err := h.policyService.CheckObjectAccess(userUUID, bucketName, objectKey, services.ActionDeleteObject)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Policy check failed",
			Message: err.Error(),
		})
		return
	}
	if !allowed {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Permission denied",
			Message: "You don't have permission to delete this object",
		})
		return
	}

	// Get object metadata from database
	var object models.Object
	if err := database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, objectKey).First(&object).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "Object not found",
		})
		return
	}

	// Get storage backend for this bucket
	storageBackend, err := h.getStorageBackend(&bucket)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to initialize storage backend",
			Message: err.Error(),
		})
		return
	}

	// Versioned delete: archive bytes + record a delete marker.
	if _, handled, derr := versionedDeleteCurrent(storageBackend, &bucket, &object); handled {
		if derr != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{
				Error:   "Failed to delete object",
				Message: derr.Error(),
			})
			return
		}
		notifyObjectEvent(&bucket, services.EventObjectRemoved, objectKey, 0, "", "")
		c.JSON(http.StatusOK, models.SuccessResponse{
			Message: "Object deleted successfully",
		})
		return
	}

	// Delete file from storage backend
	if err := storageBackend.DeleteObject(bucketName, objectKey); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to delete object from storage",
			Message: err.Error(),
		})
		return
	}

	// Delete object metadata from database
	if err := database.DB.Delete(&object).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to delete object metadata",
			Message: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: "Object deleted successfully",
	})
}

// HeadObject retrieves metadata headers for an object without downloading its content
// @Summary Get object metadata (HEAD)
// @Description Returns HTTP headers with metadata (Content-Type, Content-Length, ETag, Last-Modified) for the specified object without returning the body. Requires HeadObject permission.
// @Tags buckets
// @Param name path string true "Bucket name"
// @Param key path string true "Object key"
// @Success 200 "Object metadata headers"
// @Failure 403 "Permission denied"
// @Failure 404 "Object or bucket not found"
// @Security BearerAuth
// @Router /api/buckets/{name}/objects/{key} [head]
func (h *BucketHandler) HeadObject(c *gin.Context) {
	bucketName := c.Param("name")
	objectKey := strings.TrimPrefix(c.Param("key"), "/")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	// Get bucket from database
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		c.Status(http.StatusNotFound)
		return
	}

	// Check policy permissions
	allowed, err := h.policyService.CheckObjectAccess(userUUID, bucketName, objectKey, services.ActionHeadObject)
	if err != nil || !allowed {
		c.Status(http.StatusForbidden)
		return
	}

	// Get object metadata from database
	var object models.Object
	if err := database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, objectKey).First(&object).Error; err != nil {
		c.Status(http.StatusNotFound)
		return
	}

	// Set response headers (no body for HEAD request)
	c.Header("Content-Type", object.ContentType)
	c.Header("Content-Length", strconv.FormatInt(object.Size, 10))
	c.Header("ETag", fmt.Sprintf("\"%s\"", object.ETag))
	c.Header("Last-Modified", object.UpdatedAt.UTC().Format(http.TimeFormat))
	c.Header("Accept-Ranges", "bytes")

	c.Status(http.StatusOK)
}

// MoveObjectRequest represents the request body for moving an object
type MoveObjectRequest struct {
	SourceKey      string `json:"source_key" binding:"required"`
	DestinationKey string `json:"destination_key" binding:"required"`
}

// RenameObjectRequest represents the request body for renaming an object
type RenameObjectRequest struct {
	SourceKey string `json:"source_key" binding:"required"`
	NewName   string `json:"new_name" binding:"required"`
}

// MoveObject moves an object to a new key within the same bucket
// @Summary Move an object
// @Description Moves an object from one key to another within the same bucket. Requires GetObject, PutObject, and DeleteObject permissions on the respective keys.
// @Tags buckets
// @Accept json
// @Produce json
// @Param name path string true "Bucket name"
// @Param request body MoveObjectRequest true "Source and destination keys"
// @Success 200 {object} object "Move result with updated object"
// @Failure 400 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 409 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/buckets/{name}/objects/move [post]
func (h *BucketHandler) MoveObject(c *gin.Context) {
	bucketName := c.Param("name")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	var req MoveObjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Message: err.Error(),
		})
		return
	}

	// Validate keys
	if req.SourceKey == req.DestinationKey {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Source and destination keys cannot be the same",
		})
		return
	}

	// Get bucket from database
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "Bucket not found",
		})
		return
	}

	// Check permission to read source object
	allowed, err := h.policyService.CheckObjectAccess(userUUID, bucketName, req.SourceKey, services.ActionGetObject)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Policy check failed",
			Message: err.Error(),
		})
		return
	}
	if !allowed {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Permission denied",
			Message: "You don't have permission to read the source object",
		})
		return
	}

	// Check permission to write destination object
	allowed, err = h.policyService.CheckObjectAccess(userUUID, bucketName, req.DestinationKey, services.ActionPutObject)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Policy check failed",
			Message: err.Error(),
		})
		return
	}
	if !allowed {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Permission denied",
			Message: "You don't have permission to write to the destination",
		})
		return
	}

	// Check permission to delete source object
	allowed, err = h.policyService.CheckObjectAccess(userUUID, bucketName, req.SourceKey, services.ActionDeleteObject)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Policy check failed",
			Message: err.Error(),
		})
		return
	}
	if !allowed {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Permission denied",
			Message: "You don't have permission to delete the source object",
		})
		return
	}

	// Get source object from database
	var sourceObject models.Object
	if err := database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, req.SourceKey).First(&sourceObject).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "Source object not found",
		})
		return
	}

	// Check if destination already exists
	var existingObject models.Object
	if err := database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, req.DestinationKey).First(&existingObject).Error; err == nil {
		c.JSON(http.StatusConflict, models.ErrorResponse{
			Error: "Destination object already exists",
		})
		return
	}

	// Get storage backend
	storageBackend, err := h.getStorageBackend(&bucket)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to initialize storage backend",
			Message: err.Error(),
		})
		return
	}

	// Copy object in storage backend
	if err := storageBackend.CopyObject(bucketName, req.SourceKey, req.DestinationKey); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to copy object",
			Message: err.Error(),
		})
		return
	}

	// Delete source from storage backend
	if err := storageBackend.DeleteObject(bucketName, req.SourceKey); err != nil {
		// Try to rollback - delete the copy
		storageBackend.DeleteObject(bucketName, req.DestinationKey)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to delete source object",
			Message: err.Error(),
		})
		return
	}

	// Update database record with new key
	sourceObject.Key = req.DestinationKey
	sourceObject.UpdatedAt = time.Now()
	if err := database.DB.Save(&sourceObject).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to update object metadata",
			Message: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Object moved successfully",
		"object":  sourceObject,
	})
}

// RenameObject renames an object within its current folder
// @Summary Rename an object
// @Description Renames an object by changing its filename while keeping it in the same folder. The new name cannot contain slashes. Requires GetObject, PutObject, and DeleteObject permissions.
// @Tags buckets
// @Accept json
// @Produce json
// @Param name path string true "Bucket name"
// @Param request body RenameObjectRequest true "Source key and new filename"
// @Success 200 {object} object "Rename result with updated object"
// @Failure 400 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 409 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/buckets/{name}/objects/rename [post]
func (h *BucketHandler) RenameObject(c *gin.Context) {
	bucketName := c.Param("name")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	var req RenameObjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Message: err.Error(),
		})
		return
	}

	// Validate new name (no slashes allowed - it's just a filename)
	if strings.Contains(req.NewName, "/") {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "New name cannot contain slashes. Use move operation to change folders.",
		})
		return
	}

	// Build destination key (same folder, new filename)
	var destinationKey string
	lastSlash := strings.LastIndex(req.SourceKey, "/")
	if lastSlash >= 0 {
		destinationKey = req.SourceKey[:lastSlash+1] + req.NewName
	} else {
		destinationKey = req.NewName
	}

	if req.SourceKey == destinationKey {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "New name is the same as the current name",
		})
		return
	}

	// Get bucket from database
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "Bucket not found",
		})
		return
	}

	// Check permission to read source object
	allowed, err := h.policyService.CheckObjectAccess(userUUID, bucketName, req.SourceKey, services.ActionGetObject)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Policy check failed",
			Message: err.Error(),
		})
		return
	}
	if !allowed {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Permission denied",
			Message: "You don't have permission to read the source object",
		})
		return
	}

	// Check permission to write destination
	allowed, err = h.policyService.CheckObjectAccess(userUUID, bucketName, destinationKey, services.ActionPutObject)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Policy check failed",
			Message: err.Error(),
		})
		return
	}
	if !allowed {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Permission denied",
			Message: "You don't have permission to write to the destination",
		})
		return
	}

	// Check permission to delete source
	allowed, err = h.policyService.CheckObjectAccess(userUUID, bucketName, req.SourceKey, services.ActionDeleteObject)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Policy check failed",
			Message: err.Error(),
		})
		return
	}
	if !allowed {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Permission denied",
			Message: "You don't have permission to delete the source object",
		})
		return
	}

	// Get source object from database
	var sourceObject models.Object
	if err := database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, req.SourceKey).First(&sourceObject).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "Source object not found",
		})
		return
	}

	// Check if destination already exists
	var existingObject models.Object
	if err := database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, destinationKey).First(&existingObject).Error; err == nil {
		c.JSON(http.StatusConflict, models.ErrorResponse{
			Error: "An object with that name already exists",
		})
		return
	}

	// Get storage backend
	storageBackend, err := h.getStorageBackend(&bucket)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to initialize storage backend",
			Message: err.Error(),
		})
		return
	}

	// Copy object in storage backend
	if err := storageBackend.CopyObject(bucketName, req.SourceKey, destinationKey); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to copy object",
			Message: err.Error(),
		})
		return
	}

	// Delete source from storage backend
	if err := storageBackend.DeleteObject(bucketName, req.SourceKey); err != nil {
		// Try to rollback - delete the copy
		storageBackend.DeleteObject(bucketName, destinationKey)
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to delete source object",
			Message: err.Error(),
		})
		return
	}

	// Update database record with new key
	sourceObject.Key = destinationKey
	sourceObject.UpdatedAt = time.Now()
	if err := database.DB.Save(&sourceObject).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to update object metadata",
			Message: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "Object renamed successfully",
		"object":  sourceObject,
	})
}

// MoveFolderRequest represents the request body for moving a folder
type MoveFolderRequest struct {
	SourcePrefix      string `json:"source_prefix" binding:"required"`
	DestinationPrefix string `json:"destination_prefix" binding:"required"`
}

// MoveFolder moves all objects under a folder prefix to a new prefix
// @Summary Move a folder
// @Description Moves all objects under the specified source prefix to a new destination prefix within the same bucket. Cannot move a folder into itself.
// @Tags buckets
// @Accept json
// @Produce json
// @Param name path string true "Bucket name"
// @Param request body MoveFolderRequest true "Source and destination folder prefixes"
// @Success 200 {object} object "Move result with count of moved objects"
// @Failure 400 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/buckets/{name}/folders/move [post]
func (h *BucketHandler) MoveFolder(c *gin.Context) {
	bucketName := c.Param("name")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	var req MoveFolderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Message: err.Error(),
		})
		return
	}

	// Validate prefixes
	if req.SourcePrefix == req.DestinationPrefix {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Source and destination prefixes cannot be the same",
		})
		return
	}

	// Don't allow moving a folder into itself
	if strings.HasPrefix(req.DestinationPrefix, req.SourcePrefix) {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Cannot move a folder into itself",
		})
		return
	}

	// Get bucket from database
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "Bucket not found",
		})
		return
	}

	// Check bucket ownership or admin status
	isAdmin, _ := c.Get("is_admin")
	if bucket.OwnerID != userUUID && isAdmin != true {
		// Check policy for source folder access
		allowed, err := h.policyService.CheckObjectAccess(userUUID, bucketName, req.SourcePrefix+"*", services.ActionGetObject)
		if err != nil || !allowed {
			c.JSON(http.StatusForbidden, models.ErrorResponse{
				Error: "Permission denied",
			})
			return
		}
	}

	// Get all objects with the source prefix from database
	var sourceObjects []models.Object
	if err := database.DB.Where("bucket_id = ? AND key LIKE ?", bucket.ID, req.SourcePrefix+"%").Find(&sourceObjects).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to list source objects",
			Message: err.Error(),
		})
		return
	}

	if len(sourceObjects) == 0 {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "No objects found in source folder",
		})
		return
	}

	// Get storage backend
	storageBackend, err := h.getStorageBackend(&bucket)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to initialize storage backend",
			Message: err.Error(),
		})
		return
	}

	// Move each object
	movedCount := 0
	for _, obj := range sourceObjects {
		// Calculate new key by replacing source prefix with destination prefix
		newKey := req.DestinationPrefix + strings.TrimPrefix(obj.Key, req.SourcePrefix)

		// Copy object in storage backend
		if err := storageBackend.CopyObject(bucketName, obj.Key, newKey); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{
				Error:   "Failed to copy object",
				Message: fmt.Sprintf("Failed to copy %s: %v", obj.Key, err),
			})
			return
		}

		// Delete source from storage backend
		if err := storageBackend.DeleteObject(bucketName, obj.Key); err != nil {
			// Try to rollback - delete the copy
			storageBackend.DeleteObject(bucketName, newKey)
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{
				Error:   "Failed to delete source object",
				Message: fmt.Sprintf("Failed to delete %s: %v", obj.Key, err),
			})
			return
		}

		// Update database record with new key
		obj.Key = newKey
		obj.UpdatedAt = time.Now()
		if err := database.DB.Save(&obj).Error; err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{
				Error:   "Failed to update object metadata",
				Message: err.Error(),
			})
			return
		}

		movedCount++
	}

	c.JSON(http.StatusOK, gin.H{
		"message":     "Folder moved successfully",
		"moved_count": movedCount,
	})
}
