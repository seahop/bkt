package api

import (
	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/services"
	"bkt/internal/validation"
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	XMLName xml.Name `xml:"ListBucketResult"`
	Xmlns   string   `xml:"xmlns,attr"`
	Name    string   `xml:"Name"`
	Prefix  string   `xml:"Prefix"`
	// V1 fields
	Marker      string `xml:"Marker,omitempty"`
	NextMarker  string `xml:"NextMarker,omitempty"`
	// V2 fields (populated only when list-type=2)
	KeyCount              int    `xml:"KeyCount,omitempty"`
	ContinuationToken     string `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string `xml:"NextContinuationToken,omitempty"`
	StartAfter            string `xml:"StartAfter,omitempty"`

	Delimiter      string         `xml:"Delimiter,omitempty"`
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

type CopyObjectResult struct {
	XMLName      xml.Name  `xml:"CopyObjectResult"`
	ETag         string    `xml:"ETag"`
	LastModified time.Time `xml:"LastModified"`
}

// DeleteRequest is the XML body for POST /?delete
type DeleteRequest struct {
	XMLName xml.Name      `xml:"Delete"`
	Quiet   bool          `xml:"Quiet"`
	Objects []DeleteObject `xml:"Object"`
}

type DeleteObject struct {
	Key string `xml:"Key"`
}

type DeleteResult struct {
	XMLName xml.Name      `xml:"DeleteResult"`
	Xmlns   string        `xml:"xmlns,attr"`
	Deleted []DeletedObject `xml:"Deleted"`
	Errors  []DeleteError   `xml:"Error"`
}

type DeletedObject struct {
	Key string `xml:"Key"`
}

type DeleteError struct {
	Key     string `xml:"Key"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
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

// ListObjects handles GET /{bucket} — supports ListObjectsV1 and ListObjectsV2 (list-type=2)
func (h *S3APIHandler) ListObjects(c *gin.Context) {
	bucketName := c.Param("bucket")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		h.s3Error(c, "NoSuchBucket", "The specified bucket does not exist", bucketName, http.StatusNotFound)
		return
	}

	allowed, _ := h.policyService.CheckBucketAccess(userUUID, bucketName, services.ActionListBucket)
	if !allowed {
		h.s3Error(c, "AccessDenied", "Access Denied", bucketName, http.StatusForbidden)
		return
	}

	isV2 := c.Query("list-type") == "2"
	prefix := c.DefaultQuery("prefix", "")
	delimiter := c.Query("delimiter")

	maxKeys := 1000
	if mk := c.Query("max-keys"); mk != "" {
		if parsed, err := strconv.Atoi(mk); err == nil && parsed > 0 && parsed <= 1000 {
			maxKeys = parsed
		}
	}

	// Determine start-after key for pagination
	startAfterKey := ""
	continuationToken := ""
	if isV2 {
		continuationToken = c.Query("continuation-token")
		if continuationToken != "" {
			// continuation-token is base64(lastKey) from the previous page
			if decoded, err := base64.StdEncoding.DecodeString(continuationToken); err == nil {
				startAfterKey = string(decoded)
			}
		} else {
			startAfterKey = c.Query("start-after")
		}
	} else {
		startAfterKey = c.Query("marker")
	}

	// Query maxKeys+1 so we can detect truncation
	query := database.DB.Where("bucket_id = ?", bucket.ID)
	if prefix != "" {
		escapedPrefix := validation.EscapeLikeWildcards(prefix)
		query = query.Where("key LIKE ?", escapedPrefix+"%")
	}
	if startAfterKey != "" {
		query = query.Where("key > ?", startAfterKey)
	}

	var objects []models.Object
	if err := query.Limit(maxKeys+1).Order("key ASC").Find(&objects).Error; err != nil {
		h.s3Error(c, "InternalError", "Failed to list objects", bucketName, http.StatusInternalServerError)
		return
	}

	isTruncated := len(objects) > maxKeys
	if isTruncated {
		objects = objects[:maxKeys]
	}

	// Build contents and common prefixes
	contents := make([]ObjectInfo, 0, len(objects))
	commonPrefixes := make(map[string]bool)

	for _, obj := range objects {
		if delimiter != "" {
			keyAfterPrefix := strings.TrimPrefix(obj.Key, prefix)
			if idx := strings.Index(keyAfterPrefix, delimiter); idx >= 0 {
				commonPrefixes[prefix+keyAfterPrefix[:idx+1]] = true
				continue
			}
		}
		if strings.HasSuffix(obj.Key, "/.keep") {
			continue
		}
		contents = append(contents, ObjectInfo{
			Key:          obj.Key,
			LastModified: obj.UpdatedAt,
			ETag:         `"` + obj.ETag + `"`,
			Size:         obj.Size,
			StorageClass: "STANDARD",
			Owner: Owner{
				ID:          bucket.OwnerID.String(),
				DisplayName: bucket.Owner.Username,
			},
		})
	}

	commonPrefixList := make([]CommonPrefix, 0, len(commonPrefixes))
	for p := range commonPrefixes {
		commonPrefixList = append(commonPrefixList, CommonPrefix{Prefix: p})
	}

	response := ListBucketResult{
		Xmlns:          "http://s3.amazonaws.com/doc/2006-03-01/",
		Name:           bucketName,
		Prefix:         prefix,
		Delimiter:      delimiter,
		MaxKeys:        maxKeys,
		IsTruncated:    isTruncated,
		Contents:       contents,
		CommonPrefixes: commonPrefixList,
	}

	if isV2 {
		response.ContinuationToken = continuationToken
		response.StartAfter = c.Query("start-after")
		response.KeyCount = len(contents)
		if isTruncated && len(objects) > 0 {
			response.NextContinuationToken = base64.StdEncoding.EncodeToString([]byte(objects[len(objects)-1].Key))
		}
	} else {
		response.Marker = startAfterKey
		if isTruncated && len(objects) > 0 {
			response.NextMarker = objects[len(objects)-1].Key
		}
	}

	c.XML(http.StatusOK, response)
}

// GetObject handles GET /{bucket}/{key+} (download object or ListParts if ?uploadId present)
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

	// List parts for in-progress multipart upload
	if c.Query("uploadId") != "" {
		h.ListPartsHandler(c)
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

	// Common S3-compatible headers
	c.Header("Content-Type", object.ContentType)
	c.Header("ETag", fmt.Sprintf(`"%s"`, object.ETag))
	c.Header("Last-Modified", object.UpdatedAt.UTC().Format(http.TimeFormat))
	c.Header("Accept-Ranges", "bytes")
	c.Header("x-amz-request-id", uuid.New().String())

	// Honor a Range request if present. We advertise Accept-Ranges, so clients
	// (notably the AWS CLI/SDK, which split downloads >8MB into byte-range GETs)
	// expect 206 Partial Content. Without this they receive the full object for
	// every range and concatenate the copies into a corrupt, oversized file.
	if start, length, ok, satisfiable := parseRange(c.GetHeader("Range"), object.Size); ok {
		if !satisfiable {
			c.Header("Content-Range", fmt.Sprintf("bytes */%d", object.Size))
			h.s3Error(c, "InvalidRange", "The requested range is not satisfiable", objectKey, http.StatusRequestedRangeNotSatisfiable)
			return
		}
		// Advance to the start offset: seek when the backend supports it
		// (local files), otherwise discard the leading bytes.
		if seeker, isSeeker := file.(io.Seeker); isSeeker {
			if _, err := seeker.Seek(start, io.SeekStart); err != nil {
				h.s3Error(c, "InternalError", "Failed to seek object", objectKey, http.StatusInternalServerError)
				return
			}
		} else if _, err := io.CopyN(io.Discard, file, start); err != nil {
			h.s3Error(c, "InternalError", "Failed to read object range", objectKey, http.StatusInternalServerError)
			return
		}
		c.Header("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, object.Size))
		c.DataFromReader(http.StatusPartialContent, length, object.ContentType, io.LimitReader(file, length), nil)
		return
	}

	// Stream the full object — cap at object.Size so we never emit more bytes
	// than Content-Length declares (multipart-assembled files on disk may carry
	// trailing bytes; without the limit the client receives a corrupt, oversized body).
	c.Header("Content-Length", strconv.FormatInt(object.Size, 10))
	c.DataFromReader(http.StatusOK, object.Size, object.ContentType, io.LimitReader(file, object.Size), nil)
}

// parseRange parses a single-range HTTP Range header of the form
// "bytes=start-end", "bytes=start-", or "bytes=-suffixLength" against an object
// of the given size. It returns the absolute start offset and the number of
// bytes to send. ok is false when there is no (or a multi-range/unparseable)
// header, in which case the caller streams the full object. satisfiable is
// false when the range falls entirely outside the object (→ 416).
func parseRange(header string, size int64) (start, length int64, ok, satisfiable bool) {
	const prefix = "bytes="
	if !strings.HasPrefix(header, prefix) {
		return 0, 0, false, false
	}
	spec := strings.TrimPrefix(header, prefix)
	// Multi-range requests (comma-separated) are not supported; fall back to full.
	if strings.Contains(spec, ",") {
		return 0, 0, false, false
	}
	dash := strings.IndexByte(spec, '-')
	if dash < 0 {
		return 0, 0, false, false
	}
	startStr, endStr := spec[:dash], spec[dash+1:]

	if startStr == "" {
		// Suffix range: last N bytes.
		suffix, err := strconv.ParseInt(endStr, 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, false, false
		}
		if suffix > size {
			suffix = size
		}
		return size - suffix, suffix, true, size > 0
	}

	start, err := strconv.ParseInt(startStr, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false, false
	}
	if start >= size {
		return 0, 0, true, false // valid syntax, unsatisfiable
	}
	end := size - 1
	if endStr != "" {
		end, err = strconv.ParseInt(endStr, 10, 64)
		if err != nil || end < start {
			return 0, 0, false, false
		}
		if end > size-1 {
			end = size - 1
		}
	}
	return start, end - start + 1, true, true
}

// PutObject handles PUT /{bucket}/{key+} (upload, copy, or multipart part)
func (h *S3APIHandler) PutObject(c *gin.Context) {
	if copySource := c.GetHeader("x-amz-copy-source"); copySource != "" {
		h.CopyObject(c, copySource)
		return
	}

	// Dispatch to UploadPart for multipart uploads
	if c.Query("uploadId") != "" && c.Query("partNumber") != "" {
		h.UploadPart(c)
		return
	}

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

	// Get content length.
	// AWS CLI sends Transfer-Encoding: chunked + Content-Encoding: aws-chunked without
	// a Content-Length header. Use X-Amz-Decoded-Content-Length as the fallback.
	contentLength := c.Request.ContentLength
	if contentLength < 0 {
		if v := c.GetHeader("X-Amz-Decoded-Content-Length"); v != "" {
			contentLength, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if contentLength < 0 {
		h.s3Error(c, "MissingContentLength", "You must provide the Content-Length HTTP header", objectKey, http.StatusLengthRequired)
		return
	}

	// Check file size
	if contentLength > h.config.Storage.MaxFileSize {
		h.s3Error(c, "EntityTooLarge", "Your proposed upload exceeds the maximum allowed object size", objectKey, http.StatusRequestEntityTooLarge)
		return
	}

	// Decode aws-chunked encoding if present (AWS CLI streaming uploads)
	bodyReader := io.Reader(c.Request.Body)
	if c.GetHeader("Content-Encoding") == "aws-chunked" {
		bodyReader = newAWSChunkedReader(c.Request.Body)
	}

	// Detect actual content type from file magic numbers (don't trust client)
	detectedType, firstBytes, err := validation.DetectContentType(bodyReader)
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
	combinedReader := io.MultiReader(bytes.NewReader(firstBytes), bodyReader)

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

// DeleteObject handles DELETE /{bucket}/{key+} (delete object or abort multipart)
func (h *S3APIHandler) DeleteObject(c *gin.Context) {
	// Abort multipart upload
	if c.Query("uploadId") != "" {
		h.AbortMultipartUpload(c)
		return
	}

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

// CopyObject handles PUT /{bucket}/{key} with x-amz-copy-source header
func (h *S3APIHandler) CopyObject(c *gin.Context, copySource string) {
	destBucket := c.Param("bucket")
	destKey := strings.TrimPrefix(c.Param("key"), "/")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	// x-amz-copy-source may be URL-encoded; format is /srcBucket/srcKey
	decoded, err := url.PathUnescape(copySource)
	if err != nil {
		decoded = copySource
	}
	decoded = strings.TrimPrefix(decoded, "/")
	slashIdx := strings.Index(decoded, "/")
	if slashIdx < 0 {
		h.s3Error(c, "InvalidArgument", "Invalid copy source", copySource, http.StatusBadRequest)
		return
	}
	srcBucket := decoded[:slashIdx]
	srcKey := decoded[slashIdx+1:]

	// Permission: GetObject on source
	if allowed, _ := h.policyService.CheckObjectAccess(userUUID, srcBucket, srcKey, services.ActionGetObject); !allowed {
		h.s3Error(c, "AccessDenied", "Access Denied on source", srcKey, http.StatusForbidden)
		return
	}
	// Permission: PutObject on destination
	if allowed, _ := h.policyService.CheckObjectAccess(userUUID, destBucket, destKey, services.ActionPutObject); !allowed {
		h.s3Error(c, "AccessDenied", "Access Denied on destination", destKey, http.StatusForbidden)
		return
	}

	var srcBucketModel, destBucketModel models.Bucket
	if err := database.DB.Where("name = ?", srcBucket).First(&srcBucketModel).Error; err != nil {
		h.s3Error(c, "NoSuchBucket", "Source bucket does not exist", srcBucket, http.StatusNotFound)
		return
	}
	if err := database.DB.Where("name = ?", destBucket).First(&destBucketModel).Error; err != nil {
		h.s3Error(c, "NoSuchBucket", "Destination bucket does not exist", destBucket, http.StatusNotFound)
		return
	}

	var srcObj models.Object
	if err := database.DB.Where("bucket_id = ? AND key = ?", srcBucketModel.ID, srcKey).First(&srcObj).Error; err != nil {
		h.s3Error(c, "NoSuchKey", "Source key does not exist", srcKey, http.StatusNotFound)
		return
	}

	srcStorage, err := h.bucketHandler.getStorageBackend(&srcBucketModel)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to initialize source storage", srcKey, http.StatusInternalServerError)
		return
	}

	// If same bucket, use native CopyObject; otherwise stream copy
	if srcBucket == destBucket {
		if err := srcStorage.CopyObject(srcBucket, srcKey, destKey); err != nil {
			h.s3Error(c, "InternalError", "Failed to copy object", srcKey, http.StatusInternalServerError)
			return
		}
	} else {
		destStorage, err := h.bucketHandler.getStorageBackend(&destBucketModel)
		if err != nil {
			h.s3Error(c, "InternalError", "Failed to initialize destination storage", destKey, http.StatusInternalServerError)
			return
		}
		reader, err := srcStorage.GetObject(srcBucket, srcKey)
		if err != nil {
			h.s3Error(c, "InternalError", "Failed to read source object", srcKey, http.StatusInternalServerError)
			return
		}
		defer reader.Close()
		if err := destStorage.PutObject(destBucket, destKey, reader, srcObj.Size, srcObj.ContentType); err != nil {
			h.s3Error(c, "InternalError", "Failed to write destination object", destKey, http.StatusInternalServerError)
			return
		}
	}

	// Upsert DB metadata for destination
	destInfo, err := h.bucketHandler.getStorageBackend(&destBucketModel)
	if err == nil {
		if info, err := destInfo.GetObjectInfo(destBucket, destKey); err == nil {
			destObj := models.Object{
				BucketID:    destBucketModel.ID,
				Key:         destKey,
				Size:        info.Size,
				ContentType: info.ContentType,
				ETag:        info.ETag,
				StoragePath: destKey,
			}
			var existing models.Object
			if database.DB.Where("bucket_id = ? AND key = ?", destBucketModel.ID, destKey).First(&existing).Error == nil {
				existing.Size = destObj.Size
				existing.ContentType = destObj.ContentType
				existing.ETag = destObj.ETag
				database.DB.Save(&existing)
				destObj = existing
			} else {
				database.DB.Create(&destObj)
			}
			c.Header("ETag", fmt.Sprintf(`"%s"`, destObj.ETag))
			c.Header("x-amz-request-id", uuid.New().String())
			c.XML(http.StatusOK, CopyObjectResult{ETag: fmt.Sprintf(`"%s"`, destObj.ETag), LastModified: destObj.UpdatedAt})
			return
		}
	}

	c.Header("x-amz-request-id", uuid.New().String())
	c.XML(http.StatusOK, CopyObjectResult{ETag: fmt.Sprintf(`"%s"`, srcObj.ETag), LastModified: time.Now()})
}

// HandleBucketPost dispatches POST /{bucket} based on query params
func (h *S3APIHandler) HandleBucketPost(c *gin.Context) {
	if _, exists := c.GetQuery("delete"); exists {
		h.DeleteObjects(c)
		return
	}
	h.s3Error(c, "NotImplemented", "This operation is not implemented", "", http.StatusNotImplemented)
}

// DeleteObjects handles POST /{bucket}?delete (bulk delete)
func (h *S3APIHandler) DeleteObjects(c *gin.Context) {
	bucketName := c.Param("bucket")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		h.s3Error(c, "NoSuchBucket", "The specified bucket does not exist", bucketName, http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20)) // 1MB max
	if err != nil {
		h.s3Error(c, "MalformedXML", "Failed to read request body", "", http.StatusBadRequest)
		return
	}

	var deleteReq DeleteRequest
	if err := xml.Unmarshal(body, &deleteReq); err != nil {
		h.s3Error(c, "MalformedXML", "Invalid delete request XML", "", http.StatusBadRequest)
		return
	}

	storageBackend, err := h.bucketHandler.getStorageBackend(&bucket)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to initialize storage", "", http.StatusInternalServerError)
		return
	}

	result := DeleteResult{Xmlns: "http://s3.amazonaws.com/doc/2006-03-01/"}

	for _, obj := range deleteReq.Objects {
		allowed, _ := h.policyService.CheckObjectAccess(userUUID, bucketName, obj.Key, services.ActionDeleteObject)
		if !allowed {
			result.Errors = append(result.Errors, DeleteError{Key: obj.Key, Code: "AccessDenied", Message: "Access Denied"})
			continue
		}

		var dbObj models.Object
		if err := database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, obj.Key).First(&dbObj).Error; err != nil {
			// S3 treats deleting a non-existent key as success
			if !deleteReq.Quiet {
				result.Deleted = append(result.Deleted, DeletedObject{Key: obj.Key})
			}
			continue
		}

		if err := storageBackend.DeleteObject(bucketName, obj.Key); err != nil {
			result.Errors = append(result.Errors, DeleteError{Key: obj.Key, Code: "InternalError", Message: "Failed to delete"})
			continue
		}
		database.DB.Delete(&dbObj)

		if !deleteReq.Quiet {
			result.Deleted = append(result.Deleted, DeletedObject{Key: obj.Key})
		}
	}

	c.Header("x-amz-request-id", uuid.New().String())
	c.XML(http.StatusOK, result)
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

// awsChunkedReader decodes the AWS chunked transfer encoding (Content-Encoding: aws-chunked).
// Format per chunk: "{hex_size};chunk-signature={sig}\r\n{data}\r\n"
// Terminated by: "0;chunk-signature={sig}\r\n[trailers]\r\n\r\n"
type awsChunkedReader struct {
	r         *bufio.Reader
	remaining int
	done      bool
}

func newAWSChunkedReader(r io.Reader) *awsChunkedReader {
	return &awsChunkedReader{r: bufio.NewReaderSize(r, 32*1024)}
}

func (a *awsChunkedReader) Read(p []byte) (int, error) {
	if a.done {
		return 0, io.EOF
	}
	for a.remaining == 0 {
		// Read the chunk header line: "{hex};chunk-signature=...\r\n"
		line, err := a.r.ReadString('\n')
		if err != nil && len(line) == 0 {
			return 0, io.EOF
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		// Size is the part before the first semicolon (or the whole line for plain chunks)
		sizePart := line
		if idx := strings.IndexByte(line, ';'); idx >= 0 {
			sizePart = line[:idx]
		}
		size, parseErr := strconv.ParseInt(strings.TrimSpace(sizePart), 16, 64)
		if parseErr != nil {
			return 0, fmt.Errorf("aws-chunked: invalid chunk size %q", sizePart)
		}
		if size == 0 {
			a.done = true
			return 0, io.EOF
		}
		a.remaining = int(size)
	}

	toRead := len(p)
	if toRead > a.remaining {
		toRead = a.remaining
	}
	n, err := a.r.Read(p[:toRead])
	a.remaining -= n
	if a.remaining == 0 {
		// Consume the trailing \r\n after chunk data
		a.r.ReadString('\n')
	}
	return n, err
}
