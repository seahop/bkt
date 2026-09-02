package api

import (
	"fmt"
	"time"

	"bkt/internal/database"
	"bkt/internal/logger"
	"bkt/internal/models"
	"bkt/internal/storage"

	"github.com/google/uuid"
)

// Core versioning mechanics. Design: the `objects` table always holds the
// CURRENT state of every key (so every pre-versioning code path keeps
// working); `object_versions` holds archived prior versions and delete
// markers. Version bytes live in the backend's hidden version storage.

// archiveCurrentVersion moves the current object's bytes into version storage
// and records it as an ObjectVersion. The objects row itself is left for the
// caller to overwrite or delete. Returns the archived version's id.
func archiveCurrentVersion(backend storage.StorageBackend, bucket *models.Bucket, obj *models.Object) (string, error) {
	versionID := obj.VersionID
	if versionID == "" {
		// Pre-versioning ("null") current object: assign an id at archive time
		// so its bytes are addressable in version storage.
		versionID = uuid.New().String()
	}
	if err := backend.ArchiveObjectVersion(bucket.Name, obj.Key, versionID); err != nil {
		return "", fmt.Errorf("failed to archive current version: %w", err)
	}
	ver := models.ObjectVersion{
		BucketID:          bucket.ID,
		Key:               obj.Key,
		VersionID:         versionID,
		Size:              obj.Size,
		ContentType:       obj.ContentType,
		ETag:              obj.ETag,
		Metadata:          obj.Metadata,
		Tags:              obj.Tags,
		VersionedAt:       time.Now(),
		ContentModifiedAt: obj.UpdatedAt,
	}
	if err := database.DB.Create(&ver).Error; err != nil {
		// Best-effort undo so the current object isn't left without bytes.
		if perr := backend.PromoteObjectVersion(bucket.Name, obj.Key, versionID); perr != nil {
			logger.Warn("Version archive rollback failed — current object bytes are in version storage", map[string]interface{}{
				"bucket": bucket.Name, "key": obj.Key, "version_id": versionID, "error": perr.Error(),
			})
		}
		return "", fmt.Errorf("failed to record version: %w", err)
	}
	return versionID, nil
}

// prepareVersionedWrite archives the current version of key before an
// overwrite when the bucket has versioning enabled. Returns the archived
// version id ("" when nothing was archived) so a failed write can roll back
// with rollbackVersionedWrite.
func prepareVersionedWrite(backend storage.StorageBackend, bucket *models.Bucket, key string) (string, error) {
	if bucket.Versioning != models.VersioningEnabled {
		return "", nil
	}
	var obj models.Object
	if err := database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, key).First(&obj).Error; err != nil {
		return "", nil // no current object — nothing to archive
	}
	return archiveCurrentVersion(backend, bucket, &obj)
}

// rollbackVersionedWrite undoes prepareVersionedWrite after a failed write:
// the archived version's bytes move back to the current path and its history
// row is removed.
func rollbackVersionedWrite(backend storage.StorageBackend, bucket *models.Bucket, key, archivedVersionID string) {
	if archivedVersionID == "" {
		return
	}
	if err := backend.PromoteObjectVersion(bucket.Name, key, archivedVersionID); err != nil {
		logger.Warn("Failed to roll back archived version after write failure", map[string]interface{}{
			"bucket": bucket.Name, "key": key, "version_id": archivedVersionID, "error": err.Error(),
		})
		return
	}
	database.DB.Where("version_id = ?", archivedVersionID).Delete(&models.ObjectVersion{})
}

// versionedDeleteCurrent deletes the current object the versioned way when
// the bucket has versioning enabled: the bytes are archived and a delete
// marker is recorded; the objects row is removed (so the "current view"
// reports the key as gone, exactly like an unversioned delete would).
// Returns (markerVersionID, handled). handled=false means versioning is not
// enabled and the caller must perform its normal unversioned delete.
func versionedDeleteCurrent(backend storage.StorageBackend, bucket *models.Bucket, obj *models.Object) (string, bool, error) {
	if bucket.Versioning != models.VersioningEnabled {
		return "", false, nil
	}
	if _, err := archiveCurrentVersion(backend, bucket, obj); err != nil {
		return "", true, err
	}
	marker := models.ObjectVersion{
		BucketID:       bucket.ID,
		Key:            obj.Key,
		VersionID:      uuid.New().String(),
		IsDeleteMarker: true,
		VersionedAt:    time.Now(),
	}
	if err := database.DB.Create(&marker).Error; err != nil {
		return "", true, fmt.Errorf("failed to record delete marker: %w", err)
	}
	if err := database.DB.Delete(&models.Object{}, "id = ?", obj.ID).Error; err != nil {
		return "", true, fmt.Errorf("failed to remove current object record: %w", err)
	}
	return marker.VersionID, true, nil
}

// promoteNewestVersionIfNeeded makes the newest surviving version current when
// no current objects row exists for the key. If the newest surviving version
// is a delete marker, the key stays deleted. No-op when a current row exists.
func promoteNewestVersionIfNeeded(backend storage.StorageBackend, bucket *models.Bucket, key string) error {
	var existing models.Object
	if database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, key).First(&existing).Error == nil {
		return nil
	}
	var newest models.ObjectVersion
	if err := database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, key).
		Order("versioned_at DESC").First(&newest).Error; err != nil {
		return nil // no versions left — key is simply gone
	}
	if newest.IsDeleteMarker {
		return nil // latest surviving state is "deleted"
	}
	if err := backend.PromoteObjectVersion(bucket.Name, key, newest.VersionID); err != nil {
		return fmt.Errorf("failed to promote version: %w", err)
	}
	obj := models.Object{
		BucketID:    bucket.ID,
		Key:         key,
		Size:        newest.Size,
		ContentType: newest.ContentType,
		ETag:        newest.ETag,
		StoragePath: key,
		Metadata:    newest.Metadata,
		Tags:        newest.Tags,
		VersionID:   newest.VersionID,
		CreatedAt:   newest.ContentModifiedAt,
		UpdatedAt:   newest.ContentModifiedAt,
	}
	if err := database.DB.Create(&obj).Error; err != nil {
		return fmt.Errorf("failed to record promoted version: %w", err)
	}
	return database.DB.Delete(&models.ObjectVersion{}, "id = ?", newest.ID).Error
}

// deleteSpecificVersion permanently removes one version of a key: the current
// version (by its version id), an archived version, or a delete marker.
// Removing the current version or a latest delete marker promotes the next-
// newest surviving version, matching S3 semantics.
// retentionBlocks reports whether WORM retention forbids permanently
// removing data written at t.
func retentionBlocks(bucket *models.Bucket, t time.Time) bool {
	if bucket.RetentionDays <= 0 {
		return false
	}
	return time.Since(t) < time.Duration(bucket.RetentionDays)*24*time.Hour
}

func deleteSpecificVersion(backend storage.StorageBackend, bucket *models.Bucket, key, versionID string) error {
	// Current version addressed by id?
	var current models.Object
	if database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, key).First(&current).Error == nil {
		curID := current.VersionID
		if curID == "" {
			curID = "null"
		}
		if versionID == curID {
			if retentionBlocks(bucket, current.UpdatedAt) {
				return fmt.Errorf("object is under retention for %d days and cannot be permanently deleted yet", bucket.RetentionDays)
			}
			if err := backend.DeleteObject(bucket.Name, key); err != nil {
				return fmt.Errorf("failed to delete current version bytes: %w", err)
			}
			if err := database.DB.Delete(&models.Object{}, "id = ?", current.ID).Error; err != nil {
				return err
			}
			return promoteNewestVersionIfNeeded(backend, bucket, key)
		}
	}

	var ver models.ObjectVersion
	if err := database.DB.Where("bucket_id = ? AND key = ? AND version_id = ?", bucket.ID, key, versionID).
		First(&ver).Error; err != nil {
		return fmt.Errorf("version not found")
	}
	if !ver.IsDeleteMarker {
		if retentionBlocks(bucket, ver.ContentModifiedAt) {
			return fmt.Errorf("version is under retention for %d days and cannot be permanently deleted yet", bucket.RetentionDays)
		}
		if err := backend.DeleteObjectVersion(bucket.Name, key, ver.VersionID); err != nil {
			return err
		}
	}
	if err := database.DB.Delete(&models.ObjectVersion{}, "id = ?", ver.ID).Error; err != nil {
		return err
	}
	// Removing a delete marker can resurrect the object (S3 semantics): if the
	// key now has no current row and its newest surviving version is content,
	// promote it.
	if ver.IsDeleteMarker {
		return promoteNewestVersionIfNeeded(backend, bucket, key)
	}
	return nil
}
