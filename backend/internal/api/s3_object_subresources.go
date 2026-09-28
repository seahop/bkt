package api

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/validation"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Object-level S3 routing (/{bucket}/{key}): like the bucket level (see
// s3_bucket_subresources.go), every query sub-resource is dispatched
// explicitly. The implemented ones — ?tagging, ?uploadId (ListParts /
// UploadPart[Copy] / Complete / Abort), POST ?uploads, ?versionId, ?acl (GET,
// read-only), ?partNumber (GET/HEAD) and the response-* overrides — go to
// their handlers; any other query key is 501 NotImplemented. An unknown
// sub-resource is never answered with object content and never performs a
// write or delete.

// objectReadParams are the query keys a plain object GET/HEAD accepts.
var objectReadParams = func() map[string]bool {
	m := map[string]bool{"versionId": true, "partNumber": true}
	for _, o := range responseHeaderOverrides {
		m[o.param] = true
	}
	return m
}()

// objectDeleteParams are the query keys a plain DeleteObject accepts.
var objectDeleteParams = map[string]bool{"versionId": true}

// objectTaggingParams are the query keys the ?tagging operations accept.
var objectTaggingParams = map[string]bool{"tagging": true, "versionId": true}

// objectPartParams are the query keys UploadPart / UploadPartCopy accept.
var objectPartParams = map[string]bool{"uploadId": true, "partNumber": true}

// unsupportedObjectParam returns the first (sorted) query key that is neither
// in allowed nor a signing parameter ("" when all are acceptable).
func unsupportedObjectParam(q url.Values, allowed map[string]bool) string {
	var keys []string
	for k := range q {
		if !allowed[k] && !isSigningQueryParam(k) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	return keys[0]
}

// objectNotImplemented answers 501 for an unsupported object sub-resource
// (empty body for HEAD).
func (h *S3APIHandler) objectNotImplemented(c *gin.Context, what string) {
	c.Header("x-amz-request-id", uuid.New().String())
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusNotImplemented)
		return
	}
	h.s3Error(c, "NotImplemented", "The \""+what+"\" object sub-resource is not implemented by bkt",
		strings.TrimPrefix(c.Param("key"), "/"), http.StatusNotImplemented)
}

// failObjectSubresource answers an object request carrying a sub-resource
// bkt does not implement: 403 without a caller (the handlers' fail-closed
// rule), else 501 (HEAD: empty body).
func (h *S3APIHandler) failObjectSubresource(c *gin.Context, what string) {
	if c.Request.Method == http.MethodHead {
		if id, ok := c.Get("user_id"); !ok || id == uuid.Nil {
			c.Status(http.StatusForbidden)
			return
		}
		h.objectNotImplemented(c, what)
		return
	}
	if _, authed := h.s3Caller(c); !authed {
		return
	}
	h.objectNotImplemented(c, what)
}

// parsePartNumber validates ?partNumber on GET/HEAD. bkt stores every object
// as a single part (multipart uploads are assembled on completion), so as for
// an AWS single-part object only part 1 exists. ok=false means the error was
// answered.
func (h *S3APIHandler) parsePartNumber(c *gin.Context) (part int, ok bool) {
	raw, present := c.GetQuery("partNumber")
	if !present {
		return 0, true
	}
	key := strings.TrimPrefix(c.Param("key"), "/")
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 10000 {
		if c.Request.Method == http.MethodHead {
			c.Status(http.StatusBadRequest)
		} else {
			h.s3Error(c, "InvalidArgument", "Part number must be an integer between 1 and 10000, inclusive", key, http.StatusBadRequest)
		}
		return 0, false
	}
	if c.GetHeader("Range") != "" {
		if c.Request.Method == http.MethodHead {
			c.Status(http.StatusBadRequest)
		} else {
			h.s3Error(c, "InvalidRequest", "Cannot specify both Range header and partNumber query parameter", key, http.StatusBadRequest)
		}
		return 0, false
	}
	if n > 1 {
		if c.Request.Method == http.MethodHead {
			c.Status(http.StatusRequestedRangeNotSatisfiable)
		} else {
			h.s3Error(c, "InvalidPartNumber", "The requested partnumber is not satisfiable", key, http.StatusRequestedRangeNotSatisfiable)
		}
		return 0, false
	}
	return n, true
}

// dispatchObjectGet routes GET /{bucket}/{key}?<sub-resource>; it returns
// false for a plain object read (the caller serves the content).
func (h *S3APIHandler) dispatchObjectGet(c *gin.Context) bool {
	q := c.Request.URL.Query()
	if c.Query("uploadId") != "" {
		h.ListPartsHandler(c)
		return true
	}
	if _, ok := q["tagging"]; ok {
		if k := unsupportedObjectParam(q, objectTaggingParams); k != "" {
			h.failObjectSubresource(c, k)
			return true
		}
		h.GetObjectTagging(c)
		return true
	}
	if _, ok := q["acl"]; ok {
		h.GetObjectACL(c)
		return true
	}
	if k := unsupportedObjectParam(q, objectReadParams); k != "" {
		h.failObjectSubresource(c, k)
		return true
	}
	return false
}

// dispatchObjectHead handles unsupported HEAD sub-resources; false for a
// plain HeadObject.
func (h *S3APIHandler) dispatchObjectHead(c *gin.Context) bool {
	if k := unsupportedObjectParam(c.Request.URL.Query(), objectReadParams); k != "" {
		h.failObjectSubresource(c, k)
		return true
	}
	return false
}

// dispatchObjectPutPreflight rejects PUT /{bucket}/{key} requests carrying a
// sub-resource that PutObject / CopyObject / UploadPart[Copy] /
// PutObjectTagging do not implement, before anything is written. It returns
// true when it answered the request.
func (h *S3APIHandler) dispatchObjectPutPreflight(c *gin.Context) bool {
	q := c.Request.URL.Query()
	if _, ok := q["acl"]; ok {
		if _, authed := h.s3Caller(c); !authed {
			return true
		}
		c.Header("x-amz-request-id", uuid.New().String())
		h.s3Error(c, "NotImplemented", "PutObjectAcl is not supported: bkt objects have no ACLs; public read access is an admin setting of the bucket in the console (Bucket settings → Public read access)",
			strings.TrimPrefix(c.Param("key"), "/"), http.StatusNotImplemented)
		return true
	}
	_, hasUpload := q["uploadId"]
	_, hasPart := q["partNumber"]
	allowed := map[string]bool{}
	switch {
	case hasUpload || hasPart:
		allowed = objectPartParams
		if c.Query("uploadId") == "" || c.Query("partNumber") == "" {
			if _, authed := h.s3Caller(c); !authed {
				return true
			}
			h.s3Error(c, "InvalidArgument", "UploadPart requires both uploadId and partNumber", strings.TrimPrefix(c.Param("key"), "/"), http.StatusBadRequest)
			return true
		}
	default:
		if _, ok := q["tagging"]; ok {
			allowed = objectTaggingParams
		}
	}
	if k := unsupportedObjectParam(q, allowed); k != "" {
		h.failObjectSubresource(c, k)
		return true
	}
	return false
}

// dispatchObjectDelete routes DELETE /{bucket}/{key}?<sub-resource>; false
// for a plain DeleteObject (optionally ?versionId).
func (h *S3APIHandler) dispatchObjectDelete(c *gin.Context) bool {
	q := c.Request.URL.Query()
	if c.Query("uploadId") != "" {
		h.AbortMultipartUpload(c)
		return true
	}
	if _, ok := q["tagging"]; ok {
		if k := unsupportedObjectParam(q, objectTaggingParams); k != "" {
			h.failObjectSubresource(c, k)
			return true
		}
		h.DeleteObjectTagging(c)
		return true
	}
	if k := unsupportedObjectParam(q, objectDeleteParams); k != "" {
		h.failObjectSubresource(c, k)
		return true
	}
	return false
}

// GetObjectACL handles GET /{bucket}/{key}?acl: a read-only view (owner
// FULL_CONTROL, plus AllUsers READ while the bucket is public-read), with
// the same authorization as reading the object (s3:GetObject, public-read
// included for the current version). Anonymous callers are excluded: the
// public-read middleware admits only plain object reads.
func (h *S3APIHandler) GetObjectACL(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := strings.TrimPrefix(c.Param("key"), "/")
	userUUID, authed := h.s3Caller(c)
	if !authed {
		return
	}
	if validation.IsReservedObjectKey(objectKey) {
		h.s3Error(c, "NoSuchKey", "The specified key does not exist", objectKey, http.StatusNotFound)
		return
	}
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		h.s3Error(c, "NoSuchBucket", "The specified bucket does not exist", bucketName, http.StatusNotFound)
		return
	}
	if !h.signedReadAllowed(c, userUUID, bucketName, objectKey) {
		h.s3Error(c, "AccessDenied", "Access Denied", objectKey, http.StatusForbidden)
		return
	}

	var cur models.Object
	curErr := database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, objectKey).First(&cur).Error
	curVID := cur.VersionID
	if curVID == "" {
		curVID = "null"
	}
	vid := c.Query("versionId")
	current := vid == "" || (curErr == nil && vid == curVID)
	switch {
	case current && curErr != nil:
		h.s3Error(c, "NoSuchKey", "The specified key does not exist", objectKey, http.StatusNotFound)
		return
	case !current:
		var ver models.ObjectVersion
		if err := database.DB.Where("bucket_id = ? AND key = ? AND version_id = ? AND is_delete_marker = false", bucket.ID, objectKey, vid).
			First(&ver).Error; err != nil {
			h.s3Error(c, "NoSuchVersion", "The specified version does not exist", objectKey, http.StatusNotFound)
			return
		}
		c.Header("x-amz-version-id", vid)
	default:
		if cur.VersionID != "" {
			c.Header("x-amz-version-id", cur.VersionID)
		}
	}
	// Public read covers only the current version.
	c.Header("x-amz-request-id", uuid.New().String())
	c.XML(http.StatusOK, aclForBucket(&bucket, bucket.IsPublic && current))
}
