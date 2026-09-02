package api

import (
	"time"

	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/logger"
	"bkt/internal/models"

	"github.com/google/uuid"
)

// Replication: periodic bkt-to-bkt mirroring. A source bucket with
// ReplicateTo set has its CURRENT objects mirrored into the target bucket on
// every sweep: missing/changed objects (by ETag) are copied, and objects
// absent from the source are removed from the target. The target is fully
// managed by replication — async, idempotent, and self-healing after
// downtime. Versions/markers are not replicated (the target keeps its own
// history if IT has versioning enabled).

const replicationBatchLimit = 500

// RunReplicationSweep mirrors every replicating bucket once.
func RunReplicationSweep(cfg *config.Config) {
	h := NewBucketHandler(cfg)
	var sources []models.Bucket
	if err := database.DB.Where("replicate_to != ''").Find(&sources).Error; err != nil {
		logger.Warn("Replication sweep: failed to list buckets", map[string]interface{}{"error": err.Error()})
		return
	}
	for i := range sources {
		src := &sources[i]
		var dst models.Bucket
		if err := database.DB.Where("name = ?", src.ReplicateTo).First(&dst).Error; err != nil {
			logger.Warn("Replication: target bucket missing", map[string]interface{}{"source": src.Name, "target": src.ReplicateTo})
			continue
		}
		replicateBucket(h, src, &dst)
	}
}

func replicateBucket(h *BucketHandler, src, dst *models.Bucket) {
	srcBackend, err := h.getStorageBackend(src)
	if err != nil {
		logger.Warn("Replication: source storage init failed", map[string]interface{}{"bucket": src.Name, "error": err.Error()})
		return
	}
	dstBackend, err := h.getStorageBackend(dst)
	if err != nil {
		logger.Warn("Replication: target storage init failed", map[string]interface{}{"bucket": dst.Name, "error": err.Error()})
		return
	}

	// Diff via the DB — both sides are bkt buckets, so this is two queries.
	srcObjs := []models.Object{}
	if err := database.DB.Where("bucket_id = ?", src.ID).Find(&srcObjs).Error; err != nil {
		logger.Warn("Replication: failed to list source", map[string]interface{}{"bucket": src.Name, "error": err.Error()})
		return
	}
	dstObjs := []models.Object{}
	if err := database.DB.Where("bucket_id = ?", dst.ID).Find(&dstObjs).Error; err != nil {
		logger.Warn("Replication: failed to list target", map[string]interface{}{"bucket": dst.Name, "error": err.Error()})
		return
	}
	dstByKey := make(map[string]*models.Object, len(dstObjs))
	for i := range dstObjs {
		dstByKey[dstObjs[i].Key] = &dstObjs[i]
	}
	srcKeys := make(map[string]bool, len(srcObjs))

	copied, removed := 0, 0
	for i := range srcObjs {
		s := &srcObjs[i]
		srcKeys[s.Key] = true
		if d, ok := dstByKey[s.Key]; ok && d.ETag == s.ETag && d.Size == s.Size {
			continue // already in sync
		}
		if copied >= replicationBatchLimit {
			continue // cap per sweep; the next sweep continues
		}
		rc, err := srcBackend.GetObject(src.Name, s.Key)
		if err != nil {
			logger.Warn("Replication: read failed", map[string]interface{}{"bucket": src.Name, "key": s.Key, "error": err.Error()})
			continue
		}
		werr := dstBackend.PutObject(dst.Name, s.Key, rc, s.Size, s.ContentType, jsonPtrToMap(s.Metadata))
		rc.Close()
		if werr != nil {
			logger.Warn("Replication: write failed", map[string]interface{}{"bucket": dst.Name, "key": s.Key, "error": werr.Error()})
			continue
		}
		now := time.Now()
		newVID := ""
		if dst.Versioning == models.VersioningEnabled {
			newVID = uuid.New().String()
		}
		if d, ok := dstByKey[s.Key]; ok {
			database.DB.Model(&models.Object{}).Where("id = ?", d.ID).Updates(map[string]interface{}{
				"size": s.Size, "content_type": s.ContentType, "e_tag": s.ETag,
				"metadata": s.Metadata, "tags": s.Tags, "version_id": newVID, "updated_at": now,
			})
		} else {
			database.DB.Create(&models.Object{
				BucketID: dst.ID, Key: s.Key, Size: s.Size, ContentType: s.ContentType,
				ETag: s.ETag, StoragePath: s.Key, Metadata: s.Metadata, Tags: s.Tags,
				VersionID: newVID, CreatedAt: now, UpdatedAt: now,
			})
		}
		copied++
	}

	for key, d := range dstByKey {
		if srcKeys[key] {
			continue
		}
		if removed >= replicationBatchLimit {
			break
		}
		// Mirror the delete through the versioned path so a versioned target
		// keeps history of what replication removed.
		if _, handled, derr := versionedDeleteCurrent(dstBackend, dst, d); handled {
			if derr != nil {
				logger.Warn("Replication: versioned mirror-delete failed", map[string]interface{}{"bucket": dst.Name, "key": key, "error": derr.Error()})
				continue
			}
		} else {
			if err := dstBackend.DeleteObject(dst.Name, key); err != nil {
				logger.Warn("Replication: mirror-delete failed", map[string]interface{}{"bucket": dst.Name, "key": key, "error": err.Error()})
				continue
			}
			database.DB.Delete(&models.Object{}, "id = ?", d.ID)
		}
		removed++
	}

	if copied > 0 || removed > 0 {
		logger.Info("Replication: synced", map[string]interface{}{
			"source": src.Name, "target": dst.Name, "copied": copied, "removed": removed,
		})
	}
}
