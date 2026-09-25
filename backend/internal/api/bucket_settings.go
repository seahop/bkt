package api

import (
	"fmt"
	"net/http"
	"strings"

	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Bucket settings beyond versioning/lifecycle: quota, WORM retention,
// webhook notifications, and replication target — one PUT for the console.

type bucketSettingsRequest struct {
	QuotaBytes    *int64  `json:"quota_bytes,omitempty"`
	RetentionDays *int    `json:"retention_days,omitempty"`
	WebhookURL    *string `json:"webhook_url,omitempty"`
	WebhookSecret *string `json:"webhook_secret,omitempty"`
	WebhookEvents *string `json:"webhook_events,omitempty"` // csv of "created","removed"
	ReplicateTo   *string `json:"replicate_to,omitempty"`
}

// SetBucketSettings handles PUT /api/buckets/:name/settings.
// Only fields present in the request are changed. Each field is authorized
// separately (admin, or the matching policy action on the bucket — bucket
// ownership alone grants nothing):
//
//	quota_bytes                     s3:PutBucketQuota (bkt extension)
//	retention_days                  s3:PutBucketObjectLockConfiguration
//	webhook_url/secret/events       s3:PutBucketNotification
//	replicate_to                    s3:PutReplicationConfiguration (+ object access, see authorizeReplication)
//
// @Summary Update bucket settings
// @Description Updates quota, WORM retention, webhook notification, and replication settings. Only fields present in the body are changed. Requires admin or the matching policy action per field (s3:PutBucketQuota, s3:PutBucketObjectLockConfiguration, s3:PutBucketNotification, s3:PutReplicationConfiguration). retention_days can be raised at any time but only lowered once no data is still under retention.
// @Tags buckets
// @Accept json
// @Produce json
// @Param name path string true "Bucket name"
// @Success 200 {object} models.SuccessResponse
// @Security BearerAuth
// @Router /api/buckets/{name}/settings [put]
func (h *BucketHandler) SetBucketSettings(c *gin.Context) {
	bucketName := c.Param("name")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	var req bucketSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "Invalid request", Message: err.Error()})
		return
	}

	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{Error: "Bucket not found"})
		return
	}

	// Authorize every requested field before changing anything.
	required := []string{}
	if req.QuotaBytes != nil {
		required = append(required, services.ActionPutBucketQuota)
	}
	if req.RetentionDays != nil {
		required = append(required, services.ActionPutBucketObjectLockConfiguration)
	}
	if req.WebhookURL != nil || req.WebhookSecret != nil || req.WebhookEvents != nil {
		required = append(required, services.ActionPutBucketNotification)
	}
	if req.ReplicateTo != nil {
		required = append(required, services.ActionPutReplicationConfiguration)
	}
	for _, action := range required {
		if !authorizeBucketConfig(h.policyService, userUUID, bucket.Name, action) {
			c.JSON(http.StatusForbidden, models.ErrorResponse{
				Error:   "Permission denied",
				Message: fmt.Sprintf("Changing this setting requires admin or %s on the bucket", action),
			})
			return
		}
	}

	updates := map[string]interface{}{}

	if req.QuotaBytes != nil {
		if *req.QuotaBytes < 0 {
			c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "quota_bytes must not be negative"})
			return
		}
		updates["quota_bytes"] = *req.QuotaBytes
	}

	if req.RetentionDays != nil {
		if *req.RetentionDays < 0 {
			c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "retention_days must not be negative"})
			return
		}
		if *req.RetentionDays > 0 && bucket.Versioning != models.VersioningEnabled {
			c.JSON(http.StatusBadRequest, models.ErrorResponse{
				Error:   "Retention requires versioning",
				Message: "Enable versioning on this bucket before setting a retention period",
			})
			return
		}
		// WORM: retention can be raised at any time, but lowered/cleared only
		// once nothing is still inside the current retention window.
		if *req.RetentionDays < bucket.RetentionDays {
			if err := checkRetentionChange(bucket.RetentionDays, *req.RetentionDays, retainedDataCount(&bucket)); err != nil {
				c.JSON(http.StatusConflict, models.ErrorResponse{Error: "Retention cannot be lowered", Message: err.Error()})
				return
			}
		}
		updates["retention_days"] = *req.RetentionDays
	}

	if req.WebhookURL != nil {
		u := strings.TrimSpace(*req.WebhookURL)
		if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "webhook_url must be an http(s) URL"})
			return
		}
		updates["webhook_url"] = u
	}
	if req.WebhookSecret != nil {
		updates["webhook_secret"] = *req.WebhookSecret
	}
	if req.WebhookEvents != nil {
		for _, ev := range strings.Split(*req.WebhookEvents, ",") {
			ev = strings.TrimSpace(ev)
			if ev != "" && ev != "created" && ev != "removed" {
				c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "webhook_events entries must be 'created' or 'removed'"})
				return
			}
		}
		updates["webhook_events"] = strings.TrimSpace(*req.WebhookEvents)
	}

	if req.ReplicateTo != nil {
		target := strings.TrimSpace(*req.ReplicateTo)
		if target != "" {
			if target == bucket.Name {
				c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "A bucket cannot replicate to itself"})
				return
			}
			var targetBucket models.Bucket
			if err := database.DB.Where("name = ?", target).First(&targetBucket).Error; err != nil {
				c.JSON(http.StatusNotFound, models.ErrorResponse{Error: fmt.Sprintf("Replication target bucket %q not found", target)})
				return
			}
			// The caller must be able to do what replication will do on their
			// behalf: read the source, overwrite and delete in the target.
			if err := authorizeReplication(h.policyService, userUUID, bucket.Name, targetBucket.Name); err != nil {
				c.JSON(http.StatusForbidden, models.ErrorResponse{Error: "Permission denied", Message: err.Error()})
				return
			}
			if replicationCreatesCycle(bucket.Name, targetBucket.Name, replicateToLookup) {
				c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "Replication cycle: the target (directly or through a chain) already replicates back to this bucket"})
				return
			}
			var sourcesToTarget int64
			database.DB.Model(&models.Bucket{}).Where("replicate_to = ? AND name != ?", target, bucket.Name).Count(&sourcesToTarget)
			if sourcesToTarget > 0 {
				c.JSON(http.StatusConflict, models.ErrorResponse{
					Error:   "Target already in use",
					Message: "Another bucket already replicates into that target (a mirror target must have exactly one source)",
				})
				return
			}
		}
		updates["replicate_to"] = target
	}

	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "No settings provided"})
		return
	}
	if err := database.DB.Model(&bucket).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to update settings"})
		return
	}

	meta := map[string]interface{}{}
	for k := range updates {
		if k == "webhook_secret" {
			meta[k] = "(updated)"
			continue
		}
		meta[k] = updates[k]
	}
	_ = h.auditService.LogSuccess(c, userUUID, "", "bucket.settings", "bucket", bucket.ID.String(), bucket.Name, meta)
	c.JSON(http.StatusOK, models.SuccessResponse{Message: "Settings updated"})
}

// notifyObjectEvent enqueues a webhook event when the bucket has one
// configured for this event type.
func notifyObjectEvent(bucket *models.Bucket, event, key string, size int64, etag, versionID string) {
	if bucket.WebhookURL == "" {
		return
	}
	short := "created"
	if event == services.EventObjectRemoved {
		short = "removed"
	}
	if bucket.WebhookEvents != "" && !strings.Contains(","+bucket.WebhookEvents+",", ","+short+",") {
		return
	}
	services.EnqueueWebhook(bucket.WebhookURL, bucket.WebhookSecret, services.ObjectEvent{
		Event: event, Bucket: bucket.Name, Key: key, Size: size, ETag: etag, VersionID: versionID,
	})
}
