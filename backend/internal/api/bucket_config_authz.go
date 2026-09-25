package api

import (
	"fmt"
	"time"

	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/services"

	"github.com/google/uuid"
)

// Authorization and safety helpers for bucket-configuration endpoints
// (settings, versioning, lifecycle, replication).
//
// Bucket ownership deliberately grants nothing here: owners are (current or
// former) admins, and a demoted admin must not keep control of the buckets
// they created. Configuration changes require admin OR an explicit policy
// action (services.ActionPutBucketVersioning etc.). PolicyService's admin
// bypass reads is_admin from the database, so it reflects demotion
// immediately.

// authorizeBucketConfig reports whether the user may perform a
// bucket-configuration action on the bucket. Errors fail closed.
func authorizeBucketConfig(ps *services.PolicyService, userID uuid.UUID, bucketName, action string) bool {
	allowed, err := ps.CheckBucketAccess(userID, bucketName, action)
	return err == nil && allowed
}

// replicationAllObjectsKey is the object key used to check object-level
// permissions that must hold for EVERY key in a bucket (replication reads or
// writes arbitrary keys). It forms the resource "arn:aws:s3:::bucket/*", which
// only a grant covering the whole bucket's objects ("bucket/*", "*/*", "*")
// matches — a prefix-scoped grant does not.
const replicationAllObjectsKey = "*"

// authorizeReplication checks that a non-admin caller configuring
// source -> target replication could perform the equivalent copy themselves:
// read every object of the source, and write and delete every object of the
// target (replication overwrites and removes target objects).
func authorizeReplication(ps *services.PolicyService, userID uuid.UUID, source, target string) error {
	checks := []struct{ bucket, action string }{
		{source, services.ActionGetObject},
		{target, services.ActionPutObject},
		{target, services.ActionDeleteObject},
	}
	for _, ch := range checks {
		allowed, err := ps.CheckObjectAccess(userID, ch.bucket, replicationAllObjectsKey, ch.action)
		if err != nil || !allowed {
			return fmt.Errorf("replication requires %s on all objects of bucket %q", ch.action, ch.bucket)
		}
	}
	return nil
}

// replicationCreatesCycle reports whether making source replicate into target
// would create a replication loop: following the replicate_to chain from
// target (via next, which returns a bucket's replicate_to or "") leads back to
// source. The walk is bounded so a pre-existing loop cannot hang it.
func replicationCreatesCycle(source, target string, next func(string) string) bool {
	if source == target {
		return true
	}
	seen := map[string]bool{source: true}
	cur := target
	for i := 0; i < 64 && cur != ""; i++ {
		if seen[cur] {
			return true
		}
		seen[cur] = true
		cur = next(cur)
	}
	return false
}

// replicateToLookup returns the replicate_to of a bucket by name ("" when the
// bucket does not exist or does not replicate).
func replicateToLookup(name string) string {
	var b models.Bucket
	if err := database.DB.Select("replicate_to").Where("name = ?", name).First(&b).Error; err != nil {
		return ""
	}
	return b.ReplicateTo
}

// retainedDataCount counts current objects and archived (non-marker)
// versions of the bucket that are still inside its CURRENT retention window.
func retainedDataCount(bucket *models.Bucket) int64 {
	if bucket.RetentionDays <= 0 {
		return 0
	}
	cutoff := time.Now().AddDate(0, 0, -bucket.RetentionDays)
	var objs, vers int64
	if err := database.DB.Model(&models.Object{}).
		Where("bucket_id = ? AND updated_at > ?", bucket.ID, cutoff).Count(&objs).Error; err != nil {
		return 1 // fail closed: treat as retained
	}
	if err := database.DB.Model(&models.ObjectVersion{}).
		Where("bucket_id = ? AND is_delete_marker = false AND content_modified_at > ?", bucket.ID, cutoff).
		Count(&vers).Error; err != nil {
		return 1
	}
	return objs + vers
}

// checkRetentionChange enforces WORM semantics on retention_days: it may be
// increased at any time, but lowered (or cleared to 0) only while no data in
// the bucket is still inside the current retention window. Otherwise an
// administrator could shorten retention and then purge "retained" data.
func checkRetentionChange(current, requested int, retained int64) error {
	if requested >= current {
		return nil
	}
	if retained > 0 {
		return fmt.Errorf("retention_days cannot be lowered from %d to %d while %d object(s)/version(s) are still within the retention period", current, requested, retained)
	}
	return nil
}

// noncurrentExpiryAllowed is the pure selection rule for the lifecycle
// noncurrent-version sweep, for one entry of object_versions:
//
//   - A content version is expired unless WORM retention still covers it.
//   - A delete marker that is NOT the key's latest state is an ordinary
//     noncurrent version and is expired.
//   - The key's LATEST delete marker is its current state, not a noncurrent
//     version. It is removed only when it is the sole remaining version
//     (S3 ExpiredObjectDeleteMarker semantics). Removing it while older
//     versions remain would resurrect the newest of them — and, when that
//     version is retention-protected, current-object expiry would delete it
//     again, looping forever.
func noncurrentExpiryAllowed(isDeleteMarker, retained, isLatestMarker, otherVersionsRemain bool) bool {
	if !isDeleteMarker {
		return !retained
	}
	if isLatestMarker {
		return !otherVersionsRemain
	}
	return true
}
