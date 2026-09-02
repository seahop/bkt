package api

import (
	"encoding/xml"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/services"
	"bkt/internal/validation"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// S3 versioning API: PUT/GET ?versioning, GET ?versions, and the
// ?versionId-addressed object operations.

type versioningConfigXML struct {
	XMLName xml.Name `xml:"VersioningConfiguration"`
	Status  string   `xml:"Status"`
}

type listVersionsResult struct {
	XMLName         xml.Name           `xml:"ListVersionsResult"`
	Xmlns           string             `xml:"xmlns,attr"`
	Name            string             `xml:"Name"`
	Prefix          string             `xml:"Prefix"`
	MaxKeys         int                `xml:"MaxKeys"`
	IsTruncated     bool               `xml:"IsTruncated"`
	Versions        []versionEntryXML  `xml:"Version"`
	DeleteMarkers   []deleteMarkerXML  `xml:"DeleteMarker"`
}

type versionEntryXML struct {
	Key          string `xml:"Key"`
	VersionId    string `xml:"VersionId"`
	IsLatest     bool   `xml:"IsLatest"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
	StorageClass string `xml:"StorageClass"`
}

type deleteMarkerXML struct {
	Key          string `xml:"Key"`
	VersionId    string `xml:"VersionId"`
	IsLatest     bool   `xml:"IsLatest"`
	LastModified string `xml:"LastModified"`
}

// PutBucketVersioning handles PUT /{bucket}?versioning (owner or admin).
func (h *S3APIHandler) PutBucketVersioning(c *gin.Context) {
	bucketName := c.Param("bucket")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)
	isAdminVal, _ := c.Get("is_admin")
	admin, _ := isAdminVal.(bool)

	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		h.s3Error(c, "NoSuchBucket", "The specified bucket does not exist", bucketName, http.StatusNotFound)
		return
	}
	if !admin && bucket.OwnerID != userUUID {
		h.s3Error(c, "AccessDenied", "Only the bucket owner can change versioning", bucketName, http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 64*1024))
	if err != nil {
		h.s3Error(c, "InvalidRequest", "Failed to read request body", "", http.StatusBadRequest)
		return
	}
	var cfg versioningConfigXML
	if err := xml.Unmarshal(body, &cfg); err != nil {
		h.s3Error(c, "MalformedXML", "The XML you provided was not well-formed", "", http.StatusBadRequest)
		return
	}
	var status string
	switch strings.ToLower(cfg.Status) {
	case "enabled":
		status = models.VersioningEnabled
	case "suspended":
		status = models.VersioningSuspended
	default:
		h.s3Error(c, "MalformedXML", "Status must be Enabled or Suspended", "", http.StatusBadRequest)
		return
	}
	if status == models.VersioningSuspended && bucket.RetentionDays > 0 {
		h.s3Error(c, "InvalidBucketState", "Versioning cannot be suspended while retention is set", bucketName, http.StatusConflict)
		return
	}
	if err := database.DB.Model(&bucket).Update("versioning", status).Error; err != nil {
		h.s3Error(c, "InternalError", "Failed to update versioning", "", http.StatusInternalServerError)
		return
	}
	c.Header("x-amz-request-id", uuid.New().String())
	c.Status(http.StatusOK)
}

// ListObjectVersions handles GET /{bucket}?versions — the current object (as
// IsLatest) plus archived versions and delete markers, newest first per key.
func (h *S3APIHandler) ListObjectVersions(c *gin.Context) {
	bucketName := c.Param("bucket")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		h.s3Error(c, "NoSuchBucket", "The specified bucket does not exist", bucketName, http.StatusNotFound)
		return
	}
	if allowed, _ := h.policyService.CheckBucketAccess(userUUID, bucketName, services.ActionListBucket); !allowed {
		h.s3Error(c, "AccessDenied", "Access Denied", bucketName, http.StatusForbidden)
		return
	}

	prefix := c.Query("prefix")
	out := listVersionsResult{
		Xmlns: "http://s3.amazonaws.com/doc/2006-03-01/", Name: bucketName,
		Prefix: prefix, MaxKeys: 1000,
	}

	// Current objects are the latest versions.
	currents := []models.Object{}
	q := database.DB.Where("bucket_id = ?", bucket.ID)
	if prefix != "" {
		q = q.Where("key LIKE ?", validation.EscapeLikeWildcards(prefix)+"%")
	}
	if err := q.Order("key ASC").Limit(1000).Find(&currents).Error; err != nil {
		h.s3Error(c, "InternalError", "Failed to list objects", "", http.StatusInternalServerError)
		return
	}
	currentKeys := map[string]bool{}
	for _, o := range currents {
		vid := o.VersionID
		if vid == "" {
			vid = "null"
		}
		currentKeys[o.Key] = true
		out.Versions = append(out.Versions, versionEntryXML{
			Key: o.Key, VersionId: vid, IsLatest: true,
			LastModified: o.UpdatedAt.UTC().Format(time.RFC3339),
			ETag:         `"` + o.ETag + `"`, Size: o.Size, StorageClass: "STANDARD",
		})
	}

	vers := []models.ObjectVersion{}
	vq := database.DB.Where("bucket_id = ?", bucket.ID)
	if prefix != "" {
		vq = vq.Where("key LIKE ?", validation.EscapeLikeWildcards(prefix)+"%")
	}
	if err := vq.Order("key ASC, versioned_at DESC").Limit(10000).Find(&vers).Error; err != nil {
		h.s3Error(c, "InternalError", "Failed to list versions", "", http.StatusInternalServerError)
		return
	}
	seenLatestMarker := map[string]bool{}
	for _, v := range vers {
		if v.IsDeleteMarker {
			// The newest marker of a key with no current object is its latest state.
			isLatest := !currentKeys[v.Key] && !seenLatestMarker[v.Key]
			seenLatestMarker[v.Key] = true
			out.DeleteMarkers = append(out.DeleteMarkers, deleteMarkerXML{
				Key: v.Key, VersionId: v.VersionID, IsLatest: isLatest,
				LastModified: v.VersionedAt.UTC().Format(time.RFC3339),
			})
			continue
		}
		out.Versions = append(out.Versions, versionEntryXML{
			Key: v.Key, VersionId: v.VersionID, IsLatest: false,
			LastModified: v.ContentModifiedAt.UTC().Format(time.RFC3339),
			ETag:         `"` + v.ETag + `"`, Size: v.Size, StorageClass: "STANDARD",
		})
	}

	c.Header("x-amz-request-id", uuid.New().String())
	c.XML(http.StatusOK, out)
}

// GetObjectVersionByID handles GET/HEAD /{bucket}/{key}?versionId=X for a
// non-current version (the current version is served by the normal path).
func (h *S3APIHandler) serveObjectVersion(c *gin.Context, bucket *models.Bucket, objectKey, versionID string, headOnly bool) {
	var ver models.ObjectVersion
	if err := database.DB.Where("bucket_id = ? AND key = ? AND version_id = ?", bucket.ID, objectKey, versionID).
		First(&ver).Error; err != nil {
		h.s3Error(c, "NoSuchVersion", "The specified version does not exist", objectKey, http.StatusNotFound)
		return
	}
	if ver.IsDeleteMarker {
		c.Header("x-amz-delete-marker", "true")
		c.Header("x-amz-version-id", ver.VersionID)
		h.s3Error(c, "MethodNotAllowed", "The specified version is a delete marker", objectKey, http.StatusMethodNotAllowed)
		return
	}

	c.Header("Content-Type", ver.ContentType)
	c.Header("ETag", `"`+ver.ETag+`"`)
	c.Header("Last-Modified", ver.ContentModifiedAt.UTC().Format(http.TimeFormat))
	c.Header("x-amz-version-id", ver.VersionID)
	c.Header("x-amz-request-id", uuid.New().String())
	for k, v := range jsonPtrToMap(ver.Metadata) {
		c.Writer.Header()[amzMetaPrefix+k] = []string{v}
	}
	if headOnly {
		c.Header("Content-Length", strconv.FormatInt(ver.Size, 10))
		c.Status(http.StatusOK)
		return
	}

	backend, err := h.bucketHandler.getStorageBackend(bucket)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to initialize storage", objectKey, http.StatusInternalServerError)
		return
	}
	rc, err := backend.GetObjectVersion(bucket.Name, objectKey, versionID)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to read version", objectKey, http.StatusInternalServerError)
		return
	}
	defer rc.Close()
	c.DataFromReader(http.StatusOK, ver.Size, ver.ContentType, io.LimitReader(rc, ver.Size), nil)
}
