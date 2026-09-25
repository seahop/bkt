package api

import (
	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/logger"
	"bkt/internal/models"
	"bkt/internal/storage"
)

// Replication: periodic bkt-to-bkt mirroring. A source bucket with
// ReplicateTo set has its CURRENT objects mirrored into the target bucket on
// every sweep: missing/changed objects (by ETag) are copied, and objects
// absent from the source are removed from the target. The target is fully
// managed by replication — async, idempotent, and self-healing after
// downtime. Versions/markers are not replicated (the target keeps its own
// history if IT has versioning enabled).
//
// Safety: on a versioned target, overwrites archive the target's current
// version first (prepareVersionedWrite) and removals create delete markers,
// so replication never destroys target history — in particular a WORM
// (retention) target keeps every retained version. A target with retention
// but without versioning (not normally reachable) is never written to. The
// target's quota is enforced for copies (reserveBucketQuota). Every per-key
// copy/delete runs under the same per-key write locks as client writes. Self-replication and cycles are
// refused both when configured and here.

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
		if replicationCreatesCycle(src.Name, src.ReplicateTo, replicateToLookup) {
			logger.Warn("Replication: skipping self-replication or cycle", map[string]interface{}{"source": src.Name, "target": src.ReplicateTo})
			continue
		}
		var dst models.Bucket
		if err := database.DB.Where("name = ?", src.ReplicateTo).First(&dst).Error; err != nil {
			logger.Warn("Replication: target bucket missing", map[string]interface{}{"source": src.Name, "target": src.ReplicateTo})
			continue
		}
		if dst.RetentionDays > 0 && dst.Versioning != models.VersioningEnabled {
			logger.Warn("Replication: target has retention without versioning — refusing to write", map[string]interface{}{"source": src.Name, "target": dst.Name})
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
		if replicateObject(srcBackend, dstBackend, src, dst, s.Key) {
			copied++
		}
	}

	for key := range dstByKey {
		if srcKeys[key] {
			continue
		}
		if removed >= replicationBatchLimit {
			break
		}
		if mirrorDelete(dstBackend, src, dst, key) {
			removed++
		}
	}

	if copied > 0 || removed > 0 {
		logger.Info("Replication: synced", map[string]interface{}{
			"source": src.Name, "target": dst.Name, "copied": copied, "removed": removed,
		})
	}
}

// lockKeysAcrossBuckets locks one key in each of two buckets. Locks are taken
// in the same global order lockObjectKeys uses within a bucket (by
// bucket + "\x00" + key), so this cannot deadlock against single-bucket
// multi-key lockers or another cross-bucket locker.
func lockKeysAcrossBuckets(bucketA, keyA, bucketB, keyB string) func() {
	idA, idB := bucketA+"\x00"+keyA, bucketB+"\x00"+keyB
	if idA == idB {
		return lockObjectKeys(bucketA, keyA)
	}
	if idB < idA {
		bucketA, keyA, bucketB, keyB = bucketB, keyB, bucketA, keyA
	}
	first := lockObjectKeys(bucketA, keyA)
	second := lockObjectKeys(bucketB, keyB)
	return func() {
		second()
		first()
	}
}

// replicateObject copies the source's current object for key into the target
// under both keys' write locks (so neither the source bytes nor the target's
// current version can change mid-copy). Both rows are re-read under the lock.
// Reports whether a copy was committed.
func replicateObject(srcBackend, dstBackend storage.StorageBackend, src, dst *models.Bucket, key string) bool {
	unlock := lockKeysAcrossBuckets(src.Name, key, dst.Name, key)
	defer unlock()

	var s models.Object
	if err := database.DB.Where("bucket_id = ? AND key = ?", src.ID, key).First(&s).Error; err != nil {
		return false // removed from the source since the listing; the next sweep mirrors it
	}
	var d models.Object
	hadPrior := database.DB.Where("bucket_id = ? AND key = ?", dst.ID, key).First(&d).Error == nil
	if hadPrior && d.ETag == s.ETag && d.Size == s.Size {
		return false // already in sync
	}

	// Reserve the target's quota for the copy (released once the row is
	// committed or the copy failed).
	reservation, qerr := reserveBucketQuota(dst, s.Size)
	if qerr != nil {
		logger.Warn("Replication: target quota exceeded", map[string]interface{}{"bucket": dst.Name, "key": key, "error": qerr.Error()})
		return false
	}
	defer reservation.release()

	rc, err := srcBackend.GetObject(src.Name, key)
	if err != nil {
		logger.Warn("Replication: read failed", map[string]interface{}{"bucket": src.Name, "key": key, "error": err.Error()})
		return false
	}
	defer rc.Close() //nolint:errcheck // best-effort close of read stream

	// On a versioned target, archive the current version before the
	// overwrite so target history (and any retained data) survives.
	archivedVID, verr := prepareVersionedWrite(dstBackend, dst, key)
	if verr != nil {
		logger.Warn("Replication: failed to version target object", map[string]interface{}{"bucket": dst.Name, "key": key, "error": verr.Error()})
		return false
	}
	if werr := dstBackend.PutObject(dst.Name, key, rc, s.Size, s.ContentType, jsonPtrToMap(s.Metadata)); werr != nil {
		rollbackVersionedWrite(dstBackend, dst, key, archivedVID)
		logger.Warn("Replication: write failed", map[string]interface{}{"bucket": dst.Name, "key": key, "error": werr.Error()})
		return false
	}
	obj := models.Object{
		BucketID: dst.ID, Key: key, Size: s.Size, ContentType: s.ContentType,
		ETag: s.ETag, SHA256: s.SHA256, StoragePath: key, Metadata: s.Metadata, Tags: s.Tags,
		VersionID: newCurrentVersionID(dst),
	}
	if err := upsertCurrentObject(&obj); err != nil {
		discardFailedWrite(dstBackend, dst, key, archivedVID, hadPrior)
		logger.Warn("Replication: failed to record target object", map[string]interface{}{"bucket": dst.Name, "key": key, "error": err.Error()})
		return false
	}
	return true
}

// mirrorDelete removes key from the target (it no longer exists in the
// source) under the target key's write lock. On a versioned target this goes
// through the versioned path (bytes archived + delete marker), so target
// history — including retained data — is kept. Reports whether it removed.
func mirrorDelete(dstBackend storage.StorageBackend, src, dst *models.Bucket, key string) bool {
	unlock := lockObjectKeys(dst.Name, key)
	defer unlock()

	if currentObjectExists(src.ID, key) {
		return false // (re)created in the source since the listing
	}
	var d models.Object
	if err := database.DB.Where("bucket_id = ? AND key = ?", dst.ID, key).First(&d).Error; err != nil {
		return false // already gone
	}
	if _, handled, derr := versionedDeleteCurrent(dstBackend, dst, &d); handled {
		if derr != nil {
			logger.Warn("Replication: versioned mirror-delete failed", map[string]interface{}{"bucket": dst.Name, "key": key, "error": derr.Error()})
			return false
		}
		return true
	}
	// Unversioned target: a permanent delete. Never remove data that
	// retention still protects (defensive — retention requires versioning,
	// so this is not normally reachable).
	if retentionBlocks(dst, d.UpdatedAt) {
		return false
	}
	if err := dstBackend.DeleteObject(dst.Name, key); err != nil {
		logger.Warn("Replication: mirror-delete failed", map[string]interface{}{"bucket": dst.Name, "key": key, "error": err.Error()})
		return false
	}
	database.DB.Delete(&models.Object{}, "id = ?", d.ID)
	return true
}
