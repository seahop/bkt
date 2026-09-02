package api

import (
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"strings"
	"time"

	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/logger"
	"bkt/internal/models"
	"bkt/internal/validation"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Lifecycle: an honest subset of S3 lifecycle — one rule per bucket with
// age-based expiry of current objects (optional prefix) and permanent expiry
// of noncurrent versions. Configured via the S3 ?lifecycle subresource or the
// console REST endpoint; applied by a background sweep.

type lifecycleXML struct {
	XMLName xml.Name           `xml:"LifecycleConfiguration"`
	Rules   []lifecycleRuleXML `xml:"Rule"`
}

type lifecycleRuleXML struct {
	ID         string `xml:"ID,omitempty"`
	Status     string `xml:"Status"`
	Prefix     string `xml:"Prefix,omitempty"`
	Filter     struct {
		Prefix string `xml:"Prefix,omitempty"`
	} `xml:"Filter,omitempty"`
	Expiration struct {
		Days int `xml:"Days,omitempty"`
	} `xml:"Expiration,omitempty"`
	NoncurrentVersionExpiration struct {
		NoncurrentDays int `xml:"NoncurrentDays,omitempty"`
	} `xml:"NoncurrentVersionExpiration,omitempty"`
}

func parseLifecycleConfig(b *models.Bucket) *models.LifecycleConfig {
	if b.Lifecycle == nil || *b.Lifecycle == "" {
		return nil
	}
	var cfg models.LifecycleConfig
	if err := json.Unmarshal([]byte(*b.Lifecycle), &cfg); err != nil {
		return nil
	}
	if cfg.ExpireDays <= 0 && cfg.NoncurrentExpireDays <= 0 {
		return nil
	}
	return &cfg
}

func storeLifecycleConfig(b *models.Bucket, cfg *models.LifecycleConfig) error {
	if cfg == nil {
		return database.DB.Model(b).Update("lifecycle", nil).Error
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	s := string(raw)
	return database.DB.Model(b).Update("lifecycle", &s).Error
}

// bucketOwnerOrAdmin loads the bucket and enforces owner/admin.
func (h *S3APIHandler) bucketOwnerOrAdmin(c *gin.Context) (*models.Bucket, bool) {
	bucketName := c.Param("bucket")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)
	isAdminVal, _ := c.Get("is_admin")
	admin, _ := isAdminVal.(bool)

	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		h.s3Error(c, "NoSuchBucket", "The specified bucket does not exist", bucketName, http.StatusNotFound)
		return nil, false
	}
	if !admin && bucket.OwnerID != userUUID {
		h.s3Error(c, "AccessDenied", "Only the bucket owner can manage lifecycle", bucketName, http.StatusForbidden)
		return nil, false
	}
	return &bucket, true
}

// GetBucketLifecycle handles GET /{bucket}?lifecycle.
func (h *S3APIHandler) GetBucketLifecycle(c *gin.Context) {
	bucket, ok := h.bucketOwnerOrAdmin(c)
	if !ok {
		return
	}
	cfg := parseLifecycleConfig(bucket)
	if cfg == nil {
		h.s3Error(c, "NoSuchLifecycleConfiguration", "The lifecycle configuration does not exist", bucket.Name, http.StatusNotFound)
		return
	}
	out := lifecycleXML{}
	rule := lifecycleRuleXML{ID: "bkt-rule", Status: "Enabled", Prefix: cfg.Prefix}
	rule.Expiration.Days = cfg.ExpireDays
	rule.NoncurrentVersionExpiration.NoncurrentDays = cfg.NoncurrentExpireDays
	out.Rules = append(out.Rules, rule)
	c.Header("x-amz-request-id", uuid.New().String())
	c.XML(http.StatusOK, out)
}

// PutBucketLifecycle handles PUT /{bucket}?lifecycle (single-rule subset).
func (h *S3APIHandler) PutBucketLifecycle(c *gin.Context) {
	bucket, ok := h.bucketOwnerOrAdmin(c)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 256*1024))
	if err != nil {
		h.s3Error(c, "InvalidRequest", "Failed to read request body", "", http.StatusBadRequest)
		return
	}
	var in lifecycleXML
	if err := xml.Unmarshal(body, &in); err != nil {
		h.s3Error(c, "MalformedXML", "The XML you provided was not well-formed", "", http.StatusBadRequest)
		return
	}
	enabled := []lifecycleRuleXML{}
	for _, r := range in.Rules {
		if strings.EqualFold(r.Status, "Enabled") {
			enabled = append(enabled, r)
		}
	}
	if len(enabled) == 0 {
		if err := storeLifecycleConfig(bucket, nil); err != nil {
			h.s3Error(c, "InternalError", "Failed to store lifecycle", "", http.StatusInternalServerError)
			return
		}
		c.Status(http.StatusOK)
		return
	}
	if len(enabled) > 1 {
		h.s3Error(c, "NotImplemented", "bkt supports a single lifecycle rule per bucket", "", http.StatusNotImplemented)
		return
	}
	r := enabled[0]
	prefix := r.Prefix
	if prefix == "" {
		prefix = r.Filter.Prefix
	}
	cfg := &models.LifecycleConfig{
		ExpireDays:           r.Expiration.Days,
		Prefix:               prefix,
		NoncurrentExpireDays: r.NoncurrentVersionExpiration.NoncurrentDays,
	}
	if cfg.ExpireDays <= 0 && cfg.NoncurrentExpireDays <= 0 {
		h.s3Error(c, "MalformedXML", "Rule must set Expiration.Days or NoncurrentVersionExpiration.NoncurrentDays", "", http.StatusBadRequest)
		return
	}
	if err := storeLifecycleConfig(bucket, cfg); err != nil {
		h.s3Error(c, "InternalError", "Failed to store lifecycle", "", http.StatusInternalServerError)
		return
	}
	c.Header("x-amz-request-id", uuid.New().String())
	c.Status(http.StatusOK)
}

// DeleteBucketLifecycle handles DELETE /{bucket}?lifecycle.
func (h *S3APIHandler) DeleteBucketLifecycle(c *gin.Context) {
	bucket, ok := h.bucketOwnerOrAdmin(c)
	if !ok {
		return
	}
	if err := storeLifecycleConfig(bucket, nil); err != nil {
		h.s3Error(c, "InternalError", "Failed to delete lifecycle", "", http.StatusInternalServerError)
		return
	}
	c.Status(http.StatusNoContent)
}

// SetBucketLifecycleREST handles PUT /api/buckets/:name/lifecycle for the
// console: {"expire_days": N, "prefix": "p/", "noncurrent_expire_days": M}.
// Zero/omitted for both disables lifecycle.
// @Summary Set bucket lifecycle
// @Description Configures age-based expiry for a bucket (single rule): current objects after expire_days, noncurrent versions after noncurrent_expire_days. Both zero clears the configuration. Bucket owner or admin only.
// @Tags buckets
// @Accept json
// @Produce json
// @Param name path string true "Bucket name"
// @Success 200 {object} models.SuccessResponse
// @Security BearerAuth
// @Router /api/buckets/{name}/lifecycle [put]
func (h *BucketHandler) SetBucketLifecycleREST(c *gin.Context) {
	bucketName := c.Param("name")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)
	isAdminVal, _ := c.Get("is_admin")
	admin, _ := isAdminVal.(bool)

	var req models.LifecycleConfig
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "Invalid request", Message: err.Error()})
		return
	}
	if req.ExpireDays < 0 || req.NoncurrentExpireDays < 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "Days must not be negative"})
		return
	}
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{Error: "Bucket not found"})
		return
	}
	if !admin && bucket.OwnerID != userUUID {
		c.JSON(http.StatusForbidden, models.ErrorResponse{Error: "Only the bucket owner can manage lifecycle"})
		return
	}
	var cfg *models.LifecycleConfig
	if req.ExpireDays > 0 || req.NoncurrentExpireDays > 0 {
		cfg = &req
	}
	if err := storeLifecycleConfig(&bucket, cfg); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to store lifecycle"})
		return
	}
	h.auditService.LogSuccess(c, userUUID, "", "bucket.lifecycle", "bucket", bucket.ID.String(), bucket.Name,
		map[string]interface{}{"expire_days": req.ExpireDays, "prefix": req.Prefix, "noncurrent_expire_days": req.NoncurrentExpireDays})
	c.JSON(http.StatusOK, models.SuccessResponse{Message: "Lifecycle updated"})
}

// RunLifecycleSweep applies every bucket's lifecycle rules once. Called from
// the background scheduler; also invocable directly in tests.
func RunLifecycleSweep(cfg *config.Config) {
	h := NewBucketHandler(cfg)
	var buckets []models.Bucket
	if err := database.DB.Where("lifecycle IS NOT NULL").Find(&buckets).Error; err != nil {
		logger.Warn("Lifecycle sweep: failed to list buckets", map[string]interface{}{"error": err.Error()})
		return
	}
	for i := range buckets {
		b := &buckets[i]
		lc := parseLifecycleConfig(b)
		if lc == nil {
			continue
		}
		backend, err := h.getStorageBackend(b)
		if err != nil {
			logger.Warn("Lifecycle sweep: storage init failed", map[string]interface{}{"bucket": b.Name, "error": err.Error()})
			continue
		}

		// Expire current objects.
		if lc.ExpireDays > 0 {
			cutoff := time.Now().AddDate(0, 0, -lc.ExpireDays)
			var expired []models.Object
			q := database.DB.Where("bucket_id = ? AND updated_at < ?", b.ID, cutoff)
			if lc.Prefix != "" {
				q = q.Where("key LIKE ?", validation.EscapeLikeWildcards(lc.Prefix)+"%")
			}
			if err := q.Limit(1000).Find(&expired).Error; err == nil {
				for i := range expired {
					obj := &expired[i]
					if _, handled, derr := versionedDeleteCurrent(backend, b, obj); handled {
						if derr != nil {
							logger.Warn("Lifecycle: versioned expiry failed", map[string]interface{}{"bucket": b.Name, "key": obj.Key, "error": derr.Error()})
						}
						continue
					}
					if derr := backend.DeleteObject(b.Name, obj.Key); derr != nil {
						logger.Warn("Lifecycle: expiry failed", map[string]interface{}{"bucket": b.Name, "key": obj.Key, "error": derr.Error()})
						continue
					}
					database.DB.Delete(&models.Object{}, "id = ?", obj.ID)
				}
				if len(expired) > 0 {
					logger.Info("Lifecycle: expired current objects", map[string]interface{}{"bucket": b.Name, "count": len(expired)})
				}
			}
		}

		// Expire noncurrent versions permanently (oldest first, so a marker
		// delete cannot resurrect content that is itself due for expiry).
		if lc.NoncurrentExpireDays > 0 {
			cutoff := time.Now().AddDate(0, 0, -lc.NoncurrentExpireDays)
			var vers []models.ObjectVersion
			vq := database.DB.Where("bucket_id = ? AND versioned_at < ?", b.ID, cutoff)
			if lc.Prefix != "" {
				vq = vq.Where("key LIKE ?", validation.EscapeLikeWildcards(lc.Prefix)+"%")
			}
			if err := vq.Order("versioned_at ASC").Limit(1000).Find(&vers).Error; err == nil {
				for _, v := range vers {
					if !v.IsDeleteMarker && retentionBlocks(b, v.ContentModifiedAt) {
						continue // WORM retention outranks lifecycle expiry
					}
					if err := deleteSpecificVersion(backend, b, v.Key, v.VersionID); err != nil {
						logger.Warn("Lifecycle: version expiry failed", map[string]interface{}{"bucket": b.Name, "key": v.Key, "version": v.VersionID, "error": err.Error()})
					}
				}
				if len(vers) > 0 {
					logger.Info("Lifecycle: expired noncurrent versions", map[string]interface{}{"bucket": b.Name, "count": len(vers)})
				}
			}
		}
	}
}
