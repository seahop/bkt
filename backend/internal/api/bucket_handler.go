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

	// Check if bucket already exists
	var existing models.Bucket
	if err := database.DB.Where("name = ?", req.Name).First(&existing).Error; err == nil {
		c.JSON(http.StatusConflict, models.ErrorResponse{
			Error: "Bucket already exists",
		})
		return
	}

	// Create bucket
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
			Message: err.Error(),
		})
		return
	}

	// Create bucket in storage backend (local filesystem or S3)
	// This ensures the bucket exists in the storage backend immediately
	storageBackend, err := h.getStorageBackend(&bucket)
	if err == nil {
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
	} else {
		logger.Warn("Failed to initialize storage backend for bucket creation", map[string]interface{}{
			"bucket_name": bucket.Name,
			"error":       err.Error(),
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
			"bucket_name":     bucket.Name,
			"region":          bucket.Region,
			"storage_backend": bucket.StorageBackend,
			"is_public":       bucket.IsPublic,
		},
	)

	c.JSON(http.StatusCreated, bucket)
}

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

func (h *BucketHandler) DeleteBucket(c *gin.Context) {
	bucketName := c.Param("name")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

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

	// Use transaction to atomically check and delete (prevents TOCTOU race)
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		// Check if bucket is empty with row lock to prevent concurrent modifications
		var objectCount int64
		if err := tx.Model(&models.Object{}).
			Where("bucket_id = ?", bucket.ID).
			Count(&objectCount).Error; err != nil {
			return err
		}

		if objectCount > 0 {
			return fmt.Errorf("bucket not empty")
		}

		// Delete the bucket within the same transaction
		return tx.Delete(&bucket).Error
	})

	if err != nil {
		// Get user info for audit log
		username, _ := c.Get("username")

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
				"bucket_name": bucket.Name,
				"owner_id":    bucket.OwnerID.String(),
			},
		)

		if err.Error() == "bucket not empty" {
			c.JSON(http.StatusConflict, models.ErrorResponse{
				Error:   "Bucket not empty",
				Message: "Delete all objects before deleting the bucket",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to delete bucket",
			Message: err.Error(),
		})
		return
	}

	// Get user info for audit log
	username, _ := c.Get("username")

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
			"bucket_name": bucket.Name,
			"owner_id":    bucket.OwnerID.String(),
		},
	)

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: "Bucket deleted successfully",
	})
}

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

	// Get objects from database
	query := database.DB.Where("bucket_id = ?", bucket.ID)
	if prefix != "" {
		// Escape LIKE wildcards to prevent SQL injection via prefix parameter
		escapedPrefix := validation.EscapeLikeWildcards(prefix)
		query = query.Where("key LIKE ?", escapedPrefix+"%")
	}

	var objects []models.Object
	if err := query.Limit(maxKeys).Order("key ASC").Find(&objects).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to list objects",
			Message: err.Error(),
		})
		return
	}

	// Sync with actual storage backend to remove stale entries
	// This ensures objects deleted directly from S3 are reflected in the response
	if bucket.StorageBackend == "s3" && len(objects) > 0 {
		storageBackend, err := h.getStorageBackend(&bucket)
		if err == nil {
			// Get actual objects from S3
			s3Objects, err := storageBackend.ListObjects(bucketName, prefix)
			if err == nil {
				// Build a set of keys that exist in S3
				s3Keys := make(map[string]bool)
				for _, obj := range s3Objects {
					s3Keys[obj.Key] = true
				}

				// Filter out objects that no longer exist in S3 and delete stale DB records
				validObjects := make([]models.Object, 0, len(objects))
				staleIDs := make([]uuid.UUID, 0)
				for _, obj := range objects {
					if s3Keys[obj.Key] {
						validObjects = append(validObjects, obj)
					} else {
						staleIDs = append(staleIDs, obj.ID)
					}
				}

				// Delete stale records from database in background
				if len(staleIDs) > 0 {
					go func(ids []uuid.UUID) {
						database.DB.Where("id IN ?", ids).Delete(&models.Object{})
					}(staleIDs)
				}

				objects = validObjects
			}
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"bucket":  bucketName,
		"objects": objects,
		"count":   len(objects),
	})
}

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

	go func() {
		err := storageBackend.PutObject(bucketName, objectKey, combinedReader, fileHeader.Size, contentType)
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
		INSERT INTO objects (id, bucket_id, key, size, content_type, e_tag, storage_path, sha256, created_at, updated_at)
		VALUES (gen_random_uuid(), ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (bucket_id, key)
		DO UPDATE SET
			size = EXCLUDED.size,
			content_type = EXCLUDED.content_type,
			e_tag = EXCLUDED.e_tag,
			storage_path = EXCLUDED.storage_path,
			sha256 = EXCLUDED.sha256,
			updated_at = EXCLUDED.updated_at
	`, object.BucketID, object.Key, object.Size, object.ContentType, object.ETag,
		object.StoragePath, object.SHA256, object.CreatedAt, object.UpdatedAt).Error

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

	c.JSON(http.StatusOK, gin.H{
		"message":      "Object uploaded successfully",
		"bucket":       bucketName,
		"key":          objectKey,
		"size":         objectInfo.Size,
		"etag":         objectInfo.ETag,
		"content_type": objectInfo.ContentType,
	})
}

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
	c.Header("Content-Length", strconv.FormatInt(object.Size, 10))
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

	// Stream file to response
	c.DataFromReader(http.StatusOK, object.Size, object.ContentType, file, nil)
}

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
