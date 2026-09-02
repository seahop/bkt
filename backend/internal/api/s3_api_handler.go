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
	Key                   string `xml:"Key"`
	DeleteMarker          bool   `xml:"DeleteMarker,omitempty"`
	DeleteMarkerVersionId string `xml:"DeleteMarkerVersionId,omitempty"`
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

// LocationConstraintResult is the response body for GET /{bucket}?location.
type LocationConstraintResult struct {
	XMLName xml.Name `xml:"LocationConstraint"`
	Xmlns   string   `xml:"xmlns,attr"`
	Value   string   `xml:",chardata"`
}

// VersioningConfigurationResult is the response body for GET /{bucket}?versioning.
// An empty Status means versioning has never been enabled.
type VersioningConfigurationResult struct {
	XMLName xml.Name `xml:"VersioningConfiguration"`
	Xmlns   string   `xml:"xmlns,attr"`
	Status  string   `xml:"Status,omitempty"`
}

// bucketRegion returns the region reported for buckets. bkt is single-region;
// this reflects the configured S3 region (default us-east-1).
func (h *S3APIHandler) bucketRegion() string {
	if h.config != nil && h.config.Storage.S3.Region != "" {
		return h.config.Storage.S3.Region
	}
	return "us-east-1"
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

	// Bucket sub-resource requests share the GET /{bucket} route.
	if _, ok := c.GetQuery("location"); ok {
		region := h.bucketRegion()
		// AWS represents us-east-1 as an empty LocationConstraint.
		loc := region
		if loc == "us-east-1" {
			loc = ""
		}
		c.Header("x-amz-request-id", uuid.New().String())
		c.XML(http.StatusOK, LocationConstraintResult{
			Xmlns: "http://s3.amazonaws.com/doc/2006-03-01/",
			Value: loc,
		})
		return
	}
	if _, ok := c.GetQuery("versioning"); ok {
		status := ""
		switch bucket.Versioning {
		case models.VersioningEnabled:
			status = "Enabled"
		case models.VersioningSuspended:
			status = "Suspended"
		}
		c.Header("x-amz-request-id", uuid.New().String())
		c.XML(http.StatusOK, VersioningConfigurationResult{
			Xmlns:  "http://s3.amazonaws.com/doc/2006-03-01/",
			Status: status,
		})
		return
	}
	if _, ok := c.GetQuery("versions"); ok {
		h.ListObjectVersions(c)
		return
	}
	if _, ok := c.GetQuery("lifecycle"); ok {
		h.GetBucketLifecycle(c)
		return
	}
	if _, ok := c.GetQuery("uploads"); ok {
		h.ListMultipartUploadsHandler(c)
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

	// Tagging subresource
	if _, ok := c.GetQuery("tagging"); ok {
		h.GetObjectTagging(c)
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

	// A non-current version addressed explicitly?
	if vid := c.Query("versionId"); vid != "" {
		var cur models.Object
		curErr := database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, objectKey).First(&cur).Error
		curVID := cur.VersionID
		if curVID == "" {
			curVID = "null"
		}
		if curErr != nil || vid != curVID {
			h.serveObjectVersion(c, &bucket, objectKey, vid, false)
			return
		}
		// Addressed version IS the current one — fall through to the normal path.
	}

	// Get object metadata
	var object models.Object
	if err := database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, objectKey).First(&object).Error; err != nil {
		h.s3Error(c, "NoSuchKey", "The specified key does not exist", objectKey, http.StatusNotFound)
		return
	}
	if object.VersionID != "" {
		c.Header("x-amz-version-id", object.VersionID)
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
	defer file.Close() //nolint:errcheck // best-effort close of read stream

	// Common S3-compatible headers
	c.Header("Content-Type", object.ContentType)
	c.Header("ETag", fmt.Sprintf(`"%s"`, object.ETag))
	c.Header("Last-Modified", object.UpdatedAt.UTC().Format(http.TimeFormat))
	c.Header("Accept-Ranges", "bytes")
	c.Header("x-amz-request-id", uuid.New().String())
	writeUserMetadataHeaders(c, &object)

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
		// With uploadId+partNumber this is UploadPartCopy, not CopyObject.
		if c.Query("uploadId") != "" && c.Query("partNumber") != "" {
			h.UploadPartCopy(c)
			return
		}
		h.CopyObject(c, copySource)
		return
	}

	// Dispatch to UploadPart for multipart uploads
	if c.Query("uploadId") != "" && c.Query("partNumber") != "" {
		h.UploadPart(c)
		return
	}

	// Tagging subresource
	if _, ok := c.GetQuery("tagging"); ok {
		h.PutObjectTagging(c)
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

	// User metadata (x-amz-meta-*) and tags (x-amz-tagging). A PUT replaces
	// both wholesale, matching S3 semantics.
	userMeta, err := extractUserMetadata(c)
	if err != nil {
		h.s3Error(c, "MetadataTooLarge", err.Error(), objectKey, http.StatusBadRequest)
		return
	}
	objTags, err := parseTaggingHeader(c.GetHeader("x-amz-tagging"))
	if err != nil {
		h.s3Error(c, "InvalidTag", err.Error(), objectKey, http.StatusBadRequest)
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

	var contentType string
	var combinedReader io.Reader

	if h.config.Storage.EnforceContentTypeDetection {
		// Opt-in strict mode: detect the type from magic bytes and reject
		// "unsafe" types. Off by default — see StorageConfig.
		detectedType, firstBytes, derr := validation.DetectContentType(bodyReader)
		if derr != nil {
			h.s3Error(c, "InternalError", "Failed to detect content type", objectKey, http.StatusInternalServerError)
			return
		}
		if !validation.IsSafeContentType(detectedType) {
			h.s3Error(c, "InvalidRequest", fmt.Sprintf("File type '%s' is not allowed", detectedType), objectKey, http.StatusBadRequest)
			return
		}
		contentType = detectedType
		combinedReader = io.MultiReader(bytes.NewReader(firstBytes), bodyReader)
	} else {
		// Default S3 behavior: the client's Content-Type is authoritative
		// metadata. Fall back to a sensible default only when it's absent.
		contentType = c.GetHeader("Content-Type")
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		combinedReader = bodyReader
	}

	// Bound the stored body so a client can't exceed the max by understating
	// its declared length (e.g. via chunked encoding).
	combinedReader = &maxBytesReader{r: combinedReader, remaining: h.config.Storage.MaxFileSize}

	// Get storage backend
	storageBackend, err := h.bucketHandler.getStorageBackend(&bucket)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to initialize storage", objectKey, http.StatusInternalServerError)
		return
	}

	// Quota: reject before any bytes are written.
	if qerr := checkBucketQuota(&bucket, contentLength); qerr != nil {
		h.s3Error(c, "QuotaExceeded", qerr.Error(), objectKey, http.StatusForbidden)
		return
	}

	// Versioning: archive the current version before overwriting.
	archivedVID, verr := prepareVersionedWrite(storageBackend, &bucket, objectKey)
	if verr != nil {
		h.s3Error(c, "InternalError", "Failed to version existing object", objectKey, http.StatusInternalServerError)
		return
	}

	// Save object (use combinedReader that includes first 512 bytes)
	err = storageBackend.PutObject(bucketName, objectKey, combinedReader, contentLength, contentType, userMeta)
	if err != nil {
		rollbackVersionedWrite(storageBackend, &bucket, objectKey, archivedVID)
		h.s3Error(c, "InternalError", "Failed to save object", objectKey, http.StatusInternalServerError)
		return
	}
	newVersionID := ""
	if bucket.Versioning == models.VersioningEnabled {
		newVersionID = uuid.New().String()
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
		object.Metadata = mapToJSONPtr(userMeta)
		object.Tags = mapToJSONPtr(objTags)
		object.VersionID = newVersionID
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
			Metadata:    mapToJSONPtr(userMeta),
			Tags:        mapToJSONPtr(objTags),
			VersionID:   newVersionID,
		}
		if err := database.DB.Create(&object).Error; err != nil {
			_ = storageBackend.DeleteObject(bucketName, objectKey)
			h.s3Error(c, "InternalError", "Failed to create object metadata", objectKey, http.StatusInternalServerError)
			return
		}
	}

	notifyObjectEvent(&bucket, services.EventObjectCreated, objectKey, object.Size, object.ETag, newVersionID)

	// Return success with ETag
	c.Header("ETag", fmt.Sprintf(`"%s"`, object.ETag))
	if newVersionID != "" {
		c.Header("x-amz-version-id", newVersionID)
	}
	c.Header("x-amz-request-id", uuid.New().String())
	c.Status(http.StatusOK)
}

// DeleteObject handles DELETE /{bucket}/{key+} (delete object or abort multipart)
func (h *S3APIHandler) DeleteObject(c *gin.Context) {
	// Bucket-level DELETE sub-resources arrive with an empty key.
	if strings.TrimPrefix(c.Param("key"), "/") == "" {
		if _, ok := c.GetQuery("lifecycle"); ok {
			h.DeleteBucketLifecycle(c)
			return
		}
	}

	// Abort multipart upload
	if c.Query("uploadId") != "" {
		h.AbortMultipartUpload(c)
		return
	}

	// Tagging subresource
	if _, ok := c.GetQuery("tagging"); ok {
		h.DeleteObjectTagging(c)
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

	// Get storage backend
	storageBackend, err := h.bucketHandler.getStorageBackend(&bucket)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to get storage backend", objectKey, http.StatusInternalServerError)
		return
	}

	// Version-addressed delete: permanently removes that one version.
	if vid := c.Query("versionId"); vid != "" {
		if err := deleteSpecificVersion(storageBackend, &bucket, objectKey, vid); err != nil {
			h.s3Error(c, "InternalError", "Failed to delete version", objectKey, http.StatusInternalServerError)
			return
		}
		c.Header("x-amz-version-id", vid)
		c.Header("x-amz-request-id", uuid.New().String())
		c.Status(http.StatusNoContent)
		return
	}

	// Get object metadata
	var object models.Object
	if err := database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, objectKey).First(&object).Error; err != nil {
		// S3 returns 204 even if object doesn't exist
		c.Status(http.StatusNoContent)
		return
	}

	// Versioned delete: archive bytes + record a delete marker.
	if markerID, handled, derr := versionedDeleteCurrent(storageBackend, &bucket, &object); handled {
		if derr != nil {
			h.s3Error(c, "InternalError", "Failed to delete object", objectKey, http.StatusInternalServerError)
			return
		}
		notifyObjectEvent(&bucket, services.EventObjectRemoved, objectKey, 0, "", markerID)
		c.Header("x-amz-delete-marker", "true")
		c.Header("x-amz-version-id", markerID)
		c.Header("x-amz-request-id", uuid.New().String())
		c.Status(http.StatusNoContent)
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
	notifyObjectEvent(&bucket, services.EventObjectRemoved, objectKey, 0, "", "")

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

	// A non-current version addressed explicitly?
	if vid := c.Query("versionId"); vid != "" {
		curVID := object.VersionID
		if curVID == "" {
			curVID = "null"
		}
		if err != nil || vid != curVID {
			h.serveObjectVersion(c, &bucket, objectKey, vid, true)
			return
		}
	}
	if err == nil && object.VersionID != "" {
		c.Header("x-amz-version-id", object.VersionID)
	}

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
	writeUserMetadataHeaders(c, &object)

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

	// SDKs use x-amz-bucket-region for region discovery / redirect handling.
	c.Header("x-amz-bucket-region", h.bucketRegion())
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

	// Metadata directive: COPY (default) carries the source's user metadata to
	// the destination; REPLACE takes the request's x-amz-meta-* headers instead.
	destMeta := jsonPtrToMap(srcObj.Metadata)
	if strings.EqualFold(c.GetHeader("x-amz-metadata-directive"), "REPLACE") {
		m, merr := extractUserMetadata(c)
		if merr != nil {
			h.s3Error(c, "MetadataTooLarge", merr.Error(), destKey, http.StatusBadRequest)
			return
		}
		destMeta = m
	}

	srcStorage, err := h.bucketHandler.getStorageBackend(&srcBucketModel)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to initialize source storage", srcKey, http.StatusInternalServerError)
		return
	}

	// Versioning: archive the destination's current version before overwrite.
	destStorageForVer, dsvErr := h.bucketHandler.getStorageBackend(&destBucketModel)
	if dsvErr != nil {
		h.s3Error(c, "InternalError", "Failed to initialize destination storage", destKey, http.StatusInternalServerError)
		return
	}
	archivedVID, verr := prepareVersionedWrite(destStorageForVer, &destBucketModel, destKey)
	if verr != nil {
		h.s3Error(c, "InternalError", "Failed to version existing object", destKey, http.StatusInternalServerError)
		return
	}
	copyFailed := func() { rollbackVersionedWrite(destStorageForVer, &destBucketModel, destKey, archivedVID) }

	// If same bucket, use native CopyObject; otherwise stream copy
	if srcBucket == destBucket {
		if err := srcStorage.CopyObject(srcBucket, srcKey, destKey); err != nil {
			copyFailed()
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
		defer reader.Close() //nolint:errcheck // best-effort close of read stream
		if err := destStorage.PutObject(destBucket, destKey, reader, srcObj.Size, srcObj.ContentType, destMeta); err != nil {
			copyFailed()
			h.s3Error(c, "InternalError", "Failed to write destination object", destKey, http.StatusInternalServerError)
			return
		}
	}

	// Upsert DB metadata for destination
	destInfo, err := h.bucketHandler.getStorageBackend(&destBucketModel)
	if err == nil {
		if info, err := destInfo.GetObjectInfo(destBucket, destKey); err == nil {
			destTags := jsonPtrToMap(srcObj.Tags)
			if strings.EqualFold(c.GetHeader("x-amz-tagging-directive"), "REPLACE") {
				if t, terr := parseTaggingHeader(c.GetHeader("x-amz-tagging")); terr == nil {
					destTags = t
				}
			}
			newVID := ""
			if destBucketModel.Versioning == models.VersioningEnabled {
				newVID = uuid.New().String()
				c.Header("x-amz-version-id", newVID)
			}
			destObj := models.Object{
				BucketID:    destBucketModel.ID,
				Key:         destKey,
				Size:        info.Size,
				ContentType: info.ContentType,
				ETag:        info.ETag,
				StoragePath: destKey,
				Metadata:    mapToJSONPtr(destMeta),
				Tags:        mapToJSONPtr(destTags),
			}
			destObj.VersionID = newVID
			var existing models.Object
			if database.DB.Where("bucket_id = ? AND key = ?", destBucketModel.ID, destKey).First(&existing).Error == nil {
				existing.Size = destObj.Size
				existing.ContentType = destObj.ContentType
				existing.ETag = destObj.ETag
				existing.Metadata = destObj.Metadata
				existing.Tags = destObj.Tags
				existing.VersionID = newVID
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

		if markerID, handled, derr := versionedDeleteCurrent(storageBackend, &bucket, &dbObj); handled {
			if derr != nil {
				result.Errors = append(result.Errors, DeleteError{Key: obj.Key, Code: "InternalError", Message: "Failed to delete"})
				continue
			}
			if !deleteReq.Quiet {
				result.Deleted = append(result.Deleted, DeletedObject{Key: obj.Key, DeleteMarker: true, DeleteMarkerVersionId: markerID})
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
	if _, ok := c.GetQuery("versioning"); ok {
		h.PutBucketVersioning(c)
		return
	}
	if _, ok := c.GetQuery("lifecycle"); ok {
		h.PutBucketLifecycle(c)
		return
	}

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
		_, _ = a.r.ReadString('\n')
	}
	return n, err
}
