package api

import (
	"fmt"
	"net/http"
	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/services"
	"bkt/internal/storage"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type BucketHandler struct {
	config        *config.Config
	policyService *services.PolicyService
}

func NewBucketHandler(cfg *config.Config) *BucketHandler {
	return &BucketHandler{
		config:        cfg,
		policyService: services.NewPolicyService(),
	}
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

	// S3 backend: Check if bucket has a specific S3 configuration
	var endpoint, region, accessKeyID, secretAccessKey, bucketPrefix string
	var useSSL, forcePathStyle bool

	if bucket.S3ConfigID != nil {
		// Load bucket-specific S3 configuration from database
		var s3Config models.S3Configuration
		if err := database.DB.Where("id = ?", bucket.S3ConfigID).First(&s3Config).Error; err == nil {
			endpoint = s3Config.Endpoint
			region = s3Config.Region
			accessKeyID = s3Config.AccessKeyID
			secretAccessKey = s3Config.SecretAccessKey
			bucketPrefix = s3Config.BucketPrefix
			useSSL = s3Config.UseSSL
			forcePathStyle = s3Config.ForcePathStyle
		} else {
			// If config not found, fall back to .env
			endpoint = h.config.Storage.S3.Endpoint
			region = h.config.Storage.S3.Region
			accessKeyID = h.config.Storage.S3.AccessKeyID
			secretAccessKey = h.config.Storage.S3.SecretAccessKey
			bucketPrefix = h.config.Storage.S3.BucketPrefix
			useSSL = h.config.Storage.S3.UseSSL
			forcePathStyle = h.config.Storage.S3.ForcePathStyle
		}
	} else {
		// No specific config, use default from .env or default S3 config
		var defaultConfig models.S3Configuration
		if err := database.DB.Where("is_default = ?", true).First(&defaultConfig).Error; err == nil {
			// Use default S3 configuration from database
			endpoint = defaultConfig.Endpoint
			region = defaultConfig.Region
			accessKeyID = defaultConfig.AccessKeyID
			secretAccessKey = defaultConfig.SecretAccessKey
			bucketPrefix = defaultConfig.BucketPrefix
			useSSL = defaultConfig.UseSSL
			forcePathStyle = defaultConfig.ForcePathStyle
		} else {
			// Fall back to .env configuration
			endpoint = h.config.Storage.S3.Endpoint
			region = h.config.Storage.S3.Region
			accessKeyID = h.config.Storage.S3.AccessKeyID
			secretAccessKey = h.config.Storage.S3.SecretAccessKey
			bucketPrefix = h.config.Storage.S3.BucketPrefix
			useSSL = h.config.Storage.S3.UseSSL
			forcePathStyle = h.config.Storage.S3.ForcePathStyle
		}
	}

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
		// Fallback to local storage if initialization fails
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
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to create bucket",
			Message: err.Error(),
		})
		return
	}

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

	// Filter buckets based on policy permissions
	accessibleBuckets := make([]models.Bucket, 0)
	for _, bucket := range allBuckets {
		// Admins can see all buckets
		if isAdmin.(bool) {
			accessibleBuckets = append(accessibleBuckets, bucket)
			continue
		}

		// Check if user has ANY permission on this bucket
		// Try multiple common actions - if they have any access, they should see the bucket
		hasAccess := false
		actions := []string{
			services.ActionListBucket,
			services.ActionGetObject,
			services.ActionPutObject,
			services.ActionDeleteObject,
		}

		for _, action := range actions {
			allowed, err := h.policyService.CheckBucketAccess(userUUID, bucket.Name, action)
			if err != nil {
				continue
			}
			if allowed {
				hasAccess = true
				break
			}
		}

		if hasAccess {
			accessibleBuckets = append(accessibleBuckets, bucket)
		}
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

	// Check if bucket is empty
	var objectCount int64
	database.DB.Model(&models.Object{}).Where("bucket_id = ?", bucket.ID).Count(&objectCount)
	if objectCount > 0 {
		c.JSON(http.StatusConflict, models.ErrorResponse{
			Error:   "Bucket not empty",
			Message: "Delete all objects before deleting the bucket",
		})
		return
	}

	if err := database.DB.Delete(&bucket).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to delete bucket",
			Message: err.Error(),
		})
		return
	}

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
		query = query.Where("key LIKE ?", prefix+"%")
	}

	var objects []models.Object
	if err := query.Limit(maxKeys).Order("key ASC").Find(&objects).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to list objects",
			Message: err.Error(),
		})
		return
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

	// Check file size
	if fileHeader.Size > h.config.Storage.MaxFileSize {
		c.JSON(http.StatusRequestEntityTooLarge, models.ErrorResponse{
			Error:   "File too large",
			Message: fmt.Sprintf("Maximum file size is %d bytes", h.config.Storage.MaxFileSize),
		})
		return
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

	// Get or detect content type
	contentType := fileHeader.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
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

	// Save object using storage backend
	err = storageBackend.PutObject(bucketName, objectKey, file, fileHeader.Size, contentType)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to save object",
			Message: err.Error(),
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

	// Create or update object metadata in database
	var object models.Object
	result := database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, objectKey).First(&object)

	if result.Error == nil {
		// Update existing object
		object.Size = objectInfo.Size
		object.ContentType = objectInfo.ContentType
		object.ETag = objectInfo.ETag
		object.StoragePath = objectKey // Use key as storage path
		object.SHA256 = ""               // SHA256 not provided by storage backend
		object.UpdatedAt = time.Now()

		if err := database.DB.Save(&object).Error; err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{
				Error:   "Failed to update object metadata",
				Message: err.Error(),
			})
			return
		}
	} else {
		// Create new object
		object = models.Object{
			BucketID:    bucket.ID,
			Key:         objectKey,
			Size:        objectInfo.Size,
			ContentType: objectInfo.ContentType,
			ETag:        objectInfo.ETag,
			StoragePath: objectKey, // Use key as storage path
			SHA256:      "",         // SHA256 not provided by storage backend
		}

		if err := database.DB.Create(&object).Error; err != nil {
			// Clean up file if database insert fails
			storageBackend.DeleteObject(bucketName, objectKey)
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{
				Error:   "Failed to create object metadata",
				Message: err.Error(),
			})
			return
		}
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
