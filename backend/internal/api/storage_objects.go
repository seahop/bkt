package api

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"bkt/internal/database"
	"bkt/internal/logger"
	"bkt/internal/models"
	"bkt/internal/storage"
	"bkt/internal/validation"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

// ── Per-key write serialization ──────────────────────────────────────────────
//
// Every mutation of an object's current state (write, overwrite, delete, move)
// is a multi-step sequence — archive the current version, write the bytes,
// update the metadata row — that must not interleave with another mutation of
// the same key: two racing overwrites of a versioned key would otherwise both
// archive the same "current" version id (destroying one version's bytes), and
// racing unversioned writes could leave a metadata row describing different
// bytes than the ones on disk. lockObjectKeys serializes those sequences per
// (bucket, key) within this process. Handlers in other files that mutate
// objects (restore, version delete, lifecycle, replication) should take the
// same lock. Storage-level archive also refuses to overwrite an existing
// archived version, which covers multi-process deployments.

type keyLockEntry struct {
	mu   sync.Mutex
	refs int
}

var objectKeyLocks = struct {
	sync.Mutex
	m map[string]*keyLockEntry
}{m: map[string]*keyLockEntry{}}

// lockObjectKeys locks the given keys of one bucket and returns the unlock
// function. Keys are de-duplicated and locked in sorted order so concurrent
// multi-key lockers (moves) cannot deadlock. The returned func is idempotent.
func lockObjectKeys(bucketName string, keys ...string) func() {
	ids := make([]string, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		id := bucketName + "\x00" + k
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	entries := make([]*keyLockEntry, len(ids))
	for i, id := range ids {
		objectKeyLocks.Lock()
		e := objectKeyLocks.m[id]
		if e == nil {
			e = &keyLockEntry{}
			objectKeyLocks.m[id] = e
		}
		e.refs++
		objectKeyLocks.Unlock()
		e.mu.Lock()
		entries[i] = e
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			for i := len(entries) - 1; i >= 0; i-- {
				entries[i].mu.Unlock()
				objectKeyLocks.Lock()
				entries[i].refs--
				if entries[i].refs == 0 {
					delete(objectKeyLocks.m, ids[i])
				}
				objectKeyLocks.Unlock()
			}
		})
	}
}

// ── Metadata commit helpers ──────────────────────────────────────────────────

// upsertCurrentObject records obj as the current state of its key with a
// single INSERT ... ON CONFLICT (bucket_id, key) DO UPDATE, so a concurrent or
// pre-existing row can never make the commit fail on the unique index (the
// failure mode that left stale rows after a successful byte write). obj is
// reloaded from the database afterwards (best effort) so ID/CreatedAt reflect
// the stored row.
func upsertCurrentObject(obj *models.Object) error {
	now := time.Now()
	if obj.CreatedAt.IsZero() {
		obj.CreatedAt = now
	}
	obj.UpdatedAt = now
	if obj.StoragePath == "" {
		obj.StoragePath = obj.Key
	}
	err := database.DB.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "bucket_id"}, {Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"size", "content_type", "e_tag", "sha256", "storage_path",
			"metadata", "tags", "version_id", "updated_at",
		}),
	}).Create(obj).Error
	if err != nil {
		return err
	}
	_ = database.DB.Where("bucket_id = ? AND key = ?", obj.BucketID, obj.Key).First(obj).Error
	return nil
}

// currentObjectExists reports whether key has a current metadata row.
func currentObjectExists(bucketID uuid.UUID, key string) bool {
	var n int64
	database.DB.Model(&models.Object{}).Where("bucket_id = ? AND key = ?", bucketID, key).Count(&n)
	return n > 0
}

// discardFailedWrite undoes a byte write whose metadata commit failed:
//   - versioned overwrite: drop the new bytes and move the archived version
//     back (the key is exactly as before);
//   - brand-new key: drop the new bytes (no row, no orphan);
//   - unversioned overwrite: the previous bytes are already gone; the new
//     bytes are left in place (deleting them would leave the old row pointing
//     at nothing) and the inconsistency is logged.
func discardFailedWrite(backend storage.StorageBackend, bucket *models.Bucket, key, archivedVID string, hadPrior bool) {
	switch {
	case archivedVID != "":
		_ = backend.DeleteObject(bucket.Name, key)
		rollbackVersionedWrite(backend, bucket, key, archivedVID)
	case !hadPrior:
		_ = backend.DeleteObject(bucket.Name, key)
	default:
		logger.Warn("Object bytes overwritten but metadata commit failed; metadata may be stale", map[string]interface{}{
			"bucket": bucket.Name, "key": key,
		})
	}
}

// validateKeyForBucket applies validation.ValidateObjectKey plus the key
// rules of the bucket's storage backend: the local backend cannot represent
// S3-style folder-marker keys ending in "/" (they would alias the file of the
// same name), so they are rejected up front with a client error.
func validateKeyForBucket(bucket *models.Bucket, key string) error {
	if err := validation.ValidateObjectKey(key); err != nil {
		return err
	}
	if bucket.StorageBackend != "s3" && strings.HasSuffix(key, "/") {
		return fmt.Errorf("object keys ending in '/' are not supported by the local storage backend (folders are implicit; the console uses '<folder>/.keep')")
	}
	return nil
}

// newCurrentVersionID returns the version id for a new current version ("" =
// the S3 "null" version when versioning is not enabled).
func newCurrentVersionID(bucket *models.Bucket) string {
	if bucket.Versioning == models.VersioningEnabled {
		return uuid.New().String()
	}
	return ""
}

// moveObjectWithinBucket moves src to dstKey inside one bucket, preserving
// versioning semantics: in a versioning-enabled bucket the destination gets a
// fresh current version and the source is deleted the versioned way (bytes
// archived + delete marker), exactly as a CopyObject + DeleteObject pair
// would; otherwise the metadata row is re-keyed and the source bytes removed.
//
// Preconditions (caller): holds lockObjectKeys for src.Key and dstKey; has
// verified dstKey has no current row; has applied permission and retention
// checks. On failure it restores the previous state as far as possible.
func moveObjectWithinBucket(backend storage.StorageBackend, bucket *models.Bucket, src *models.Object, dstKey string) (*models.Object, error) {
	if err := backend.CopyObject(bucket.Name, src.Key, dstKey); err != nil {
		return nil, fmt.Errorf("failed to copy %s: %w", src.Key, err)
	}
	now := time.Now()

	if bucket.Versioning == models.VersioningEnabled {
		dst := models.Object{
			BucketID:    bucket.ID,
			Key:         dstKey,
			Size:        src.Size,
			ContentType: src.ContentType,
			ETag:        src.ETag,
			SHA256:      src.SHA256,
			StoragePath: dstKey,
			Metadata:    src.Metadata,
			Tags:        src.Tags,
			VersionID:   uuid.New().String(),
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := database.DB.Create(&dst).Error; err != nil {
			_ = backend.DeleteObject(bucket.Name, dstKey)
			return nil, fmt.Errorf("failed to record %s: %w", dstKey, err)
		}
		if _, _, err := versionedDeleteCurrent(backend, bucket, src); err != nil {
			database.DB.Delete(&models.Object{}, "id = ?", dst.ID)
			_ = backend.DeleteObject(bucket.Name, dstKey)
			return nil, fmt.Errorf("failed to remove source %s: %w", src.Key, err)
		}
		return &dst, nil
	}

	// Unversioned: re-key the row, then remove the source bytes.
	oldKey, oldPath, oldUpdated := src.Key, src.StoragePath, src.UpdatedAt
	res := database.DB.Model(&models.Object{}).Where("id = ? AND key = ?", src.ID, oldKey).
		Updates(map[string]interface{}{"key": dstKey, "storage_path": dstKey, "updated_at": now})
	if res.Error != nil || res.RowsAffected != 1 {
		_ = backend.DeleteObject(bucket.Name, dstKey)
		if res.Error != nil {
			return nil, fmt.Errorf("failed to update metadata for %s: %w", oldKey, res.Error)
		}
		return nil, fmt.Errorf("source %s changed during move", oldKey)
	}
	if err := backend.DeleteObject(bucket.Name, oldKey); err != nil {
		database.DB.Model(&models.Object{}).Where("id = ?", src.ID).
			Updates(map[string]interface{}{"key": oldKey, "storage_path": oldPath, "updated_at": oldUpdated})
		_ = backend.DeleteObject(bucket.Name, dstKey)
		return nil, fmt.Errorf("failed to delete source %s: %w", oldKey, err)
	}
	moved := *src
	moved.Key = dstKey
	moved.StoragePath = dstKey
	moved.UpdatedAt = now
	return &moved, nil
}

// ── Bucket response views ────────────────────────────────────────────────────

// bucketOwnerView is the owner summary shown to non-admin callers — never the
// full User record (email, SSO identifiers, admin flag).
type bucketOwnerView struct {
	ID       uuid.UUID `json:"id"`
	Username string    `json:"username"`
}

// bucketView is the bucket representation returned to non-admin callers. It
// omits the storage configuration reference (s3_config_id) and the owner's
// user record, and includes the webhook URL (often a bearer secret, e.g. Slack
// incoming webhooks) and the replication target only for callers allowed to
// change them — so the settings form can still prefill for those users.
type bucketView struct {
	ID             uuid.UUID        `json:"id"`
	Name           string           `json:"name"`
	OwnerID        uuid.UUID        `json:"owner_id"`
	IsPublic       bool             `json:"is_public"`
	Region         string           `json:"region"`
	StorageBackend string           `json:"storage_backend"`
	Versioning     string           `json:"versioning"`
	Lifecycle      *string          `json:"lifecycle,omitempty"`
	QuotaBytes     int64            `json:"quota_bytes"`
	RetentionDays  int              `json:"retention_days"`
	WebhookURL     string           `json:"webhook_url,omitempty"`
	WebhookEvents  string           `json:"webhook_events,omitempty"`
	ReplicateTo    string           `json:"replicate_to,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
	Owner          *bucketOwnerView `json:"owner,omitempty"`
}

func newBucketView(b *models.Bucket, showNotification, showReplication bool) bucketView {
	v := bucketView{
		ID:             b.ID,
		Name:           b.Name,
		OwnerID:        b.OwnerID,
		IsPublic:       b.IsPublic,
		Region:         b.Region,
		StorageBackend: b.StorageBackend,
		Versioning:     b.Versioning,
		Lifecycle:      b.Lifecycle,
		QuotaBytes:     b.QuotaBytes,
		RetentionDays:  b.RetentionDays,
		CreatedAt:      b.CreatedAt,
		UpdatedAt:      b.UpdatedAt,
	}
	if b.Owner.ID != uuid.Nil {
		v.Owner = &bucketOwnerView{ID: b.Owner.ID, Username: b.Owner.Username}
	}
	if showNotification {
		v.WebhookURL = b.WebhookURL
		v.WebhookEvents = b.WebhookEvents
	}
	if showReplication {
		v.ReplicateTo = b.ReplicateTo
	}
	return v
}
