package api

import (
	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/services"
	"bkt/internal/validation"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// S3APIHandler handles S3-compatible API requests
type S3APIHandler struct {
	config        *config.Config
	policyService *services.PolicyService
	bucketHandler *BucketHandler
}

func NewS3APIHandler(cfg *config.Config) *S3APIHandler {
	return &S3APIHandler{
		config:        cfg,
		policyService: services.NewPolicyService(),
		bucketHandler: NewBucketHandler(cfg),
	}
}

// S3 XML response structures
type ListAllMyBucketsResult struct {
	XMLName xml.Name `xml:"ListAllMyBucketsResult"`
	Xmlns   string   `xml:"xmlns,attr"`
	Owner   Owner    `xml:"Owner"`
	Buckets Buckets  `xml:"Buckets"`
}

type Owner struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

type Buckets struct {
	Bucket []BucketInfo `xml:"Bucket"`
}

type BucketInfo struct {
	Name         string    `xml:"Name"`
	CreationDate time.Time `xml:"CreationDate"`
}

type ListBucketResult struct {
	XMLName        xml.Name       `xml:"ListBucketResult"`
	Xmlns          string         `xml:"xmlns,attr"`
	Name           string         `xml:"Name"`
	Prefix         string         `xml:"Prefix"`
	MaxKeys        int            `xml:"MaxKeys"`
	IsTruncated    bool           `xml:"IsTruncated"`
	Contents       []ObjectInfo   `xml:"Contents"`
	CommonPrefixes []CommonPrefix `xml:"CommonPrefixes"`
}

type ObjectInfo struct {
	Key          string    `xml:"Key"`
	LastModified time.Time `xml:"LastModified"`
	ETag         string    `xml:"ETag"`
	Size         int64     `xml:"Size"`
	StorageClass string    `xml:"StorageClass"`
	Owner        Owner     `xml:"Owner"`
}

type CommonPrefix struct {
	Prefix string `xml:"Prefix"`
}

type Error struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	Resource  string   `xml:"Resource,omitempty"`
	RequestID string   `xml:"RequestId,omitempty"`
}

// ListBuckets handles GET / (list all buckets)
func (h *S3APIHandler) ListBuckets(c *gin.Context) {
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)
	isAdmin, _ := c.Get("is_admin")

	var allBuckets []models.Bucket
	if err := database.DB.Preload("Owner").Find(&allBuckets).Error; err != nil {
		h.s3Error(c, "InternalError", "Failed to list buckets", "", http.StatusInternalServerError)
		return
	}

	// Use batch permission check to avoid N+1 queries (fixes CRITICAL performance issue)
	var accessibleBuckets []models.Bucket
	if isAdmin.(bool) {
		// Admin bypass - return all buckets
		accessibleBuckets = allBuckets
	} else {
		// Batch check which buckets user can list
		var err error
		accessibleBuckets, err = h.policyService.FilterAccessibleBuckets(userUUID, allBuckets, services.ActionListBucket)
		if err != nil {
			h.s3Error(c, "InternalError", "Failed to check bucket permissions", "", http.StatusInternalServerError)
			return
		}
	}

	// Build XML response
	user, _ := c.Get("user")
	userModel := user.(*models.User)

	bucketInfos := make([]BucketInfo, len(accessibleBuckets))
	for i, bucket := range accessibleBuckets {
		bucketInfos[i] = BucketInfo{
			Name:         bucket.Name,
			CreationDate: bucket.CreatedAt,
		}
	}

	response := ListAllMyBucketsResult{
		Xmlns: "http://s3.amazonaws.com/doc/2006-03-01/",
		Owner: Owner{
			ID:          userModel.ID.String(),
			DisplayName: userModel.Username,
		},
		Buckets: Buckets{
			Bucket: bucketInfos,
		},
	}

	c.XML(http.StatusOK, response)
}

// ListObjects handles GET /{bucket} (list objects in bucket)
func (h *S3APIHandler) ListObjects(c *gin.Context) {
	bucketName := c.Param("bucket")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	// Get bucket
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		h.s3Error(c, "NoSuchBucket", "The specified bucket does not exist", bucketName, http.StatusNotFound)
		return
	}

	// Check permissions
	allowed, _ := h.policyService.CheckBucketAccess(userUUID, bucketName, services.ActionListBucket)
	if !allowed {
		h.s3Error(c, "AccessDenied", "Access Denied", bucketName, http.StatusForbidden)
		return
	}

	// Parse query parameters
	prefix := c.DefaultQuery("prefix", "")
	delimiter := c.Query("delimiter")
	maxKeys := 1000
	if mk := c.Query("max-keys"); mk != "" {
		if parsed, err := strconv.Atoi(mk); err == nil && parsed > 0 {
			maxKeys = parsed
		}
	}

	// Get objects from database
	var objects []models.Object
	query := database.DB.Where("bucket_id = ?", bucket.ID)
	if prefix != "" {
		// Escape LIKE wildcards to prevent SQL injection via prefix parameter
		escapedPrefix := validation.EscapeLikeWildcards(prefix)
		query = query.Where("key LIKE ?", escapedPrefix+"%")
	}
	if err := query.Limit(maxKeys).Order("key ASC").Find(&objects).Error; err != nil {
		h.s3Error(c, "InternalError", "Failed to list objects", bucketName, http.StatusInternalServerError)
		return
	}

	// Build response
	contents := make([]ObjectInfo, 0)
	commonPrefixes := make(map[string]bool)

	for _, obj := range objects {
		// Handle delimiter (for directory-like listing) - do this BEFORE skipping .keep files
		// so that folders with only .keep files still show up as prefixes
		if delimiter != "" {
			keyAfterPrefix := strings.TrimPrefix(obj.Key, prefix)
			if idx := strings.Index(keyAfterPrefix, delimiter); idx >= 0 {
				// This is inside a "directory"
				commonPrefix := prefix + keyAfterPrefix[:idx+1]
				commonPrefixes[commonPrefix] = true
				continue
			}
		}

		// Skip .keep files from the contents list (but they were already processed for commonPrefixes above)
		if strings.HasSuffix(obj.Key, "/.keep") {
			continue
		}

		contents = append(contents, ObjectInfo{
			Key:          obj.Key,
			LastModified: obj.UpdatedAt,
			ETag:         fmt.Sprintf(`"%s"`, obj.ETag),
			Size:         obj.Size,
			StorageClass: "STANDARD",
			Owner: Owner{
				ID:          bucket.OwnerID.String(),
				DisplayName: bucket.Owner.Username,
			},
		})
	}

	// Convert common prefixes map to slice
	commonPrefixList := make([]CommonPrefix, 0, len(commonPrefixes))
	for prefix := range commonPrefixes {
		commonPrefixList = append(commonPrefixList, CommonPrefix{Prefix: prefix})
	}

	response := ListBucketResult{
		Xmlns:          "http://s3.amazonaws.com/doc/2006-03-01/",
		Name:           bucketName,
		Prefix:         prefix,
		MaxKeys:        maxKeys,
		IsTruncated:    false,
		Contents:       contents,
		CommonPrefixes: commonPrefixList,
	}

	c.XML(http.StatusOK, response)
}

// GetObject handles GET /{bucket}/{key+} (download object)
func (h *S3APIHandler) GetObject(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := c.Param("key")

	// Trim leading slash (Gin's * wildcard includes it)
	objectKey = strings.TrimPrefix(objectKey, "/")

	// If key is empty, this is a ListObjects request
	if objectKey == "" {
		h.ListObjects(c)
		return
	}

	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	// Get bucket
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		h.s3Error(c, "NoSuchBucket", "The specified bucket does not exist", bucketName, http.StatusNotFound)
		return
	}

	// Check permissions
	allowed, _ := h.policyService.CheckObjectAccess(userUUID, bucketName, objectKey, services.ActionGetObject)
	if !allowed {
		h.s3Error(c, "AccessDenied", "Access Denied", objectKey, http.StatusForbidden)
		return
	}

	// Get object metadata
	var object models.Object
	if err := database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, objectKey).First(&object).Error; err != nil {
		h.s3Error(c, "NoSuchKey", "The specified key does not exist", objectKey, http.StatusNotFound)
		return
	}

	// Get storage backend
	storageBackend, err := h.bucketHandler.getStorageBackend(&bucket)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to initialize storage", objectKey, http.StatusInternalServerError)
		return
	}

	// Get object from storage
	file, err := storageBackend.GetObject(bucketName, objectKey)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to retrieve object", objectKey, http.StatusInternalServerError)
		return
	}
	defer file.Close()

	// Set S3-compatible headers
	c.Header("Content-Type", object.ContentType)
	c.Header("Content-Length", strconv.FormatInt(object.Size, 10))
	c.Header("ETag", fmt.Sprintf(`"%s"`, object.ETag))
	c.Header("Last-Modified", object.UpdatedAt.UTC().Format(http.TimeFormat))
	c.Header("Accept-Ranges", "bytes")
	c.Header("x-amz-request-id", uuid.New().String())

	// Stream file
	c.DataFromReader(http.StatusOK, object.Size, object.ContentType, file, nil)
}

// PutObject handles PUT /{bucket}/{key+} (upload object)
func (h *S3APIHandler) PutObject(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := strings.TrimPrefix(c.Param("key"), "/")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	// Validate object key to prevent path traversal and other attacks
	if err := validation.ValidateObjectKey(objectKey); err != nil {
		h.s3Error(c, "InvalidArgument", err.Error(), objectKey, http.StatusBadRequest)
		return
	}

	// Get bucket
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		h.s3Error(c, "NoSuchBucket", "The specified bucket does not exist", bucketName, http.StatusNotFound)
		return
	}

	// Check permissions
	allowed, _ := h.policyService.CheckObjectAccess(userUUID, bucketName, objectKey, services.ActionPutObject)
	if !allowed {
		h.s3Error(c, "AccessDenied", "Access Denied", objectKey, http.StatusForbidden)
		return
	}

	// Get content length
	contentLength := c.Request.ContentLength
	if contentLength < 0 {
		h.s3Error(c, "MissingContentLength", "You must provide the Content-Length HTTP header", objectKey, http.StatusLengthRequired)
		return
	}

	// Check file size
	if contentLength > h.config.Storage.MaxFileSize {
		h.s3Error(c, "EntityTooLarge", "Your proposed upload exceeds the maximum allowed object size", objectKey, http.StatusRequestEntityTooLarge)
		return
	}

	// Detect actual content type from file magic numbers (don't trust client)
	detectedType, firstBytes, err := validation.DetectContentType(c.Request.Body)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to detect content type", objectKey, http.StatusInternalServerError)
		return
	}

	// Validate content type is safe
	if !validation.IsSafeContentType(detectedType) {
		h.s3Error(c, "InvalidRequest", fmt.Sprintf("File type '%s' is not allowed", detectedType), objectKey, http.StatusBadRequest)
		return
	}

	// Use detected content type (from magic numbers, not from client header)
	contentType := detectedType

	// Create MultiReader to prepend the first bytes back to the stream
	combinedReader := io.MultiReader(bytes.NewReader(firstBytes), c.Request.Body)

	// Get storage backend
	storageBackend, err := h.bucketHandler.getStorageBackend(&bucket)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to initialize storage", objectKey, http.StatusInternalServerError)
		return
	}

	// Save object (use combinedReader that includes first 512 bytes)
	err = storageBackend.PutObject(bucketName, objectKey, combinedReader, contentLength, contentType)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to save object", objectKey, http.StatusInternalServerError)
		return
	}

	// Get object info (including ETag)
	objectInfo, err := storageBackend.GetObjectInfo(bucketName, objectKey)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to get object info", objectKey, http.StatusInternalServerError)
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
		object.StoragePath = objectKey
		object.UpdatedAt = time.Now()
		database.DB.Save(&object)
	} else {
		// Create new object
		object = models.Object{
			BucketID:    bucket.ID,
			Key:         objectKey,
			Size:        objectInfo.Size,
			ContentType: objectInfo.ContentType,
			ETag:        objectInfo.ETag,
			StoragePath: objectKey,
		}
		if err := database.DB.Create(&object).Error; err != nil {
			storageBackend.DeleteObject(bucketName, objectKey)
			h.s3Error(c, "InternalError", "Failed to create object metadata", objectKey, http.StatusInternalServerError)
			return
		}
	}

	// Return success with ETag
	c.Header("ETag", fmt.Sprintf(`"%s"`, object.ETag))
	c.Header("x-amz-request-id", uuid.New().String())
	c.Status(http.StatusOK)
}

// DeleteObject handles DELETE /{bucket}/{key+} (delete object)
func (h *S3APIHandler) DeleteObject(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := strings.TrimPrefix(c.Param("key"), "/")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	// Get bucket
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		// S3 returns 204 even if bucket doesn't exist
		c.Status(http.StatusNoContent)
		return
	}

	// Check permissions
	allowed, _ := h.policyService.CheckObjectAccess(userUUID, bucketName, objectKey, services.ActionDeleteObject)
	if !allowed {
		h.s3Error(c, "AccessDenied", "Access Denied", objectKey, http.StatusForbidden)
		return
	}

	// Get object metadata
	var object models.Object
	if err := database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, objectKey).First(&object).Error; err != nil {
		// S3 returns 204 even if object doesn't exist
		c.Status(http.StatusNoContent)
		return
	}

	// Get storage backend
	storageBackend, err := h.bucketHandler.getStorageBackend(&bucket)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to get storage backend", objectKey, http.StatusInternalServerError)
		return
	}

	// Delete from storage first - MUST succeed before database delete (prevents inconsistency)
	if err := storageBackend.DeleteObject(bucketName, objectKey); err != nil {
		h.s3Error(c, "InternalError", "Failed to delete object from storage", objectKey, http.StatusInternalServerError)
		return
	}

	// Delete from database only after storage delete succeeds
	if err := database.DB.Delete(&object).Error; err != nil {
		// Critical: storage deleted but database failed - log this for manual cleanup
		h.s3Error(c, "InternalError", "Failed to delete object metadata", objectKey, http.StatusInternalServerError)
		return
	}

	c.Header("x-amz-request-id", uuid.New().String())
	c.Status(http.StatusNoContent)
}

// HeadObject handles HEAD /{bucket}/{key+} (get object metadata)
func (h *S3APIHandler) HeadObject(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := strings.TrimPrefix(c.Param("key"), "/")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	// Get bucket
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		c.Status(http.StatusNotFound)
		return
	}

	// Check permissions
	allowed, _ := h.policyService.CheckObjectAccess(userUUID, bucketName, objectKey, services.ActionGetObject)
	if !allowed {
		c.Status(http.StatusForbidden)
		return
	}

	// Get object metadata
	var object models.Object
	err := database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, objectKey).First(&object).Error

	// If exact match not found and key ends with /, it might be a folder
	// Check if any objects exist with this prefix
	if err != nil && strings.HasSuffix(objectKey, "/") {
		var count int64
		database.DB.Model(&models.Object{}).Where("bucket_id = ? AND key LIKE ?", bucket.ID, objectKey+"%").Count(&count)
		if count > 0 {
			// It's a folder - return folder-like metadata
			c.Header("Content-Type", "application/x-directory")
			c.Header("Content-Length", "0")
			c.Header("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
			c.Header("x-amz-request-id", uuid.New().String())
			c.Status(http.StatusOK)
			return
		}
	}

	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}

	// Set headers for regular object
	c.Header("Content-Type", object.ContentType)
	c.Header("Content-Length", strconv.FormatInt(object.Size, 10))
	c.Header("ETag", fmt.Sprintf(`"%s"`, object.ETag))
	c.Header("Last-Modified", object.UpdatedAt.UTC().Format(http.TimeFormat))
	c.Header("Accept-Ranges", "bytes")
	c.Header("x-amz-request-id", uuid.New().String())

	c.Status(http.StatusOK)
}

// HeadBucket handles HEAD /{bucket} (check if bucket exists)
func (h *S3APIHandler) HeadBucket(c *gin.Context) {
	bucketName := c.Param("bucket")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	// Get bucket
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		c.Status(http.StatusNotFound)
		return
	}

	// Check permissions
	allowed, _ := h.policyService.CheckBucketAccess(userUUID, bucketName, services.ActionListBucket)
	if !allowed {
		c.Status(http.StatusForbidden)
		return
	}

	c.Header("x-amz-request-id", uuid.New().String())
	c.Status(http.StatusOK)
}

// s3Error sends an S3-compatible XML error response
func (h *S3APIHandler) s3Error(c *gin.Context, code, message, resource string, status int) {
	errorResponse := Error{
		Code:      code,
		Message:   message,
		Resource:  resource,
		RequestID: uuid.New().String(),
	}
	c.XML(status, errorResponse)
}

// CreateBucket handles PUT /{bucket} (create bucket)
// NOTE: For now, we don't allow bucket creation via S3 API (only via web UI)
func (h *S3APIHandler) CreateBucket(c *gin.Context) {
	h.s3Error(c, "AccessDenied", "Bucket creation via S3 API is not supported. Use web UI.", "", http.StatusForbidden)
}
