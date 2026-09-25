package api

import (
	"net/http"
	"strconv"
	"time"

	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/services"
	"bkt/internal/validation"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// deletedObjectJSON is one "deleted but recoverable" key: its newest version
// is a delete marker and it has no current version.
type deletedObjectJSON struct {
	Key string `json:"key"`
	// DeleteMarkerVersionID is the version id of the latest delete marker;
	// permanently deleting that version (DELETE /object-versions) restores
	// the newest remaining version.
	DeleteMarkerVersionID string    `json:"delete_marker_version_id"`
	DeletedAt             time.Time `json:"deleted_at"`
	// Recoverable reports whether a content version survives below the
	// marker (lifecycle expiry may have removed every noncurrent version).
	Recoverable  bool       `json:"recoverable"`
	Size         int64      `json:"size"`
	ContentType  string     `json:"content_type,omitempty"`
	LastModified *time.Time `json:"last_modified,omitempty"`
}

// ListDeletedObjects handles GET /api/buckets/:name/deleted-objects
// ?prefix=&max_keys=&continuation_token= — keys under prefix whose latest
// version is a delete marker, in key order with keyset pagination (the token
// is the last key of the previous page). Authorization mirrors the console
// object listing: ListBucket on the bucket.
// @Summary List deleted objects
// @Description Lists keys under a prefix whose latest version is a delete marker (deleted but recoverable on versioned buckets), with the marker's version id and the size/last-modified of the newest surviving version. Requires ListBucket on the bucket.
// @Tags buckets
// @Produce json
// @Param name path string true "Bucket name"
// @Param prefix query string false "Key prefix"
// @Param max_keys query int false "Page size (1-1000, default 1000)"
// @Param continuation_token query string false "Last key of the previous page"
// @Success 200 {object} object
// @Security BearerAuth
// @Router /api/buckets/{name}/deleted-objects [get]
func (h *BucketHandler) ListDeletedObjects(c *gin.Context) {
	bucketName := c.Param("name")
	userUUID := c.MustGet("user_id").(uuid.UUID)

	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{Error: "Bucket not found"})
		return
	}
	allowed, err := h.policyService.CheckBucketAccess(userUUID, bucketName, services.ActionListBucket)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Policy check failed", Message: err.Error()})
		return
	}
	if !allowed {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Permission denied",
			Message: "You don't have permission to list objects in this bucket",
		})
		return
	}

	prefix := c.Query("prefix")
	maxKeys := 1000
	if mk := c.Query("max_keys"); mk != "" {
		if n, err := strconv.Atoi(mk); err == nil && n > 0 && n <= 1000 {
			maxKeys = n
		}
	}
	token := c.Query("continuation_token")

	rows, err := loadDeletedKeys(bucket.ID, prefix, token, maxKeys+1)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to list deleted objects", Message: err.Error()})
		return
	}
	truncated := len(rows) > maxKeys
	if truncated {
		rows = rows[:maxKeys]
	}
	nextToken := ""
	if truncated && len(rows) > 0 {
		nextToken = rows[len(rows)-1].Key
	}

	c.JSON(http.StatusOK, gin.H{
		"bucket":                  bucket.Name,
		"prefix":                  prefix,
		"versioning":              bucket.Versioning,
		"objects":                 rows,
		"count":                   len(rows),
		"is_truncated":            truncated,
		"next_continuation_token": nextToken,
	})
}

// loadDeletedKeys returns up to limit deleted keys (see ListDeletedObjects)
// under prefix and after afterKey, in key order.
func loadDeletedKeys(bucketID uuid.UUID, prefix, afterKey string, limit int) ([]deletedObjectJSON, error) {
	// Newest entry per key (same total order as every version listing,
	// versionOrder), kept only when it is a delete marker and the key has no
	// current row. The (bucket_id, key) index drives both the prefix range
	// and the per-key grouping.
	type markerRow struct {
		Key         string
		VersionID   string
		VersionedAt time.Time
	}
	var markers []markerRow
	if err := database.DB.Raw(`
		SELECT l.key, l.version_id, l.versioned_at FROM (
			SELECT DISTINCT ON (key) key, version_id, is_delete_marker, versioned_at
			FROM object_versions
			WHERE bucket_id = ? AND key LIKE ? AND key > ?
			ORDER BY key, versioned_at DESC, id DESC
		) l
		WHERE l.is_delete_marker
		  AND NOT EXISTS (SELECT 1 FROM objects o WHERE o.bucket_id = ? AND o.key = l.key)
		ORDER BY l.key
		LIMIT ?`,
		bucketID, validation.EscapeLikeWildcards(prefix)+"%", afterKey, bucketID, limit,
	).Scan(&markers).Error; err != nil {
		return nil, err
	}

	out := make([]deletedObjectJSON, 0, len(markers))
	if len(markers) == 0 {
		return out, nil
	}
	keys := make([]string, 0, len(markers))
	for _, m := range markers {
		keys = append(keys, m.Key)
	}

	// Newest surviving content version of each key (what a restore brings back).
	type contentRow struct {
		Key               string
		Size              int64
		ContentType       string
		ContentModifiedAt time.Time
	}
	var contents []contentRow
	if err := database.DB.Raw(`
		SELECT DISTINCT ON (key) key, size, content_type, content_modified_at
		FROM object_versions
		WHERE bucket_id = ? AND key IN ? AND NOT is_delete_marker
		ORDER BY key, versioned_at DESC, id DESC`,
		bucketID, keys,
	).Scan(&contents).Error; err != nil {
		return nil, err
	}
	byKey := make(map[string]contentRow, len(contents))
	for _, cr := range contents {
		byKey[cr.Key] = cr
	}

	for _, m := range markers {
		if validation.IsReservedObjectKey(m.Key) {
			continue
		}
		entry := deletedObjectJSON{Key: m.Key, DeleteMarkerVersionID: m.VersionID, DeletedAt: m.VersionedAt}
		if cr, ok := byKey[m.Key]; ok {
			lm := cr.ContentModifiedAt
			entry.Recoverable = true
			entry.Size = cr.Size
			entry.ContentType = cr.ContentType
			entry.LastModified = &lm
		}
		out = append(out, entry)
	}
	return out, nil
}
