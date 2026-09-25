package api

import (
	"fmt"
	"sync"

	"bkt/internal/database"
	"bkt/internal/logger"
	"bkt/internal/models"

	"github.com/google/uuid"
)

// Bucket quota enforcement for object writes.
//
// Usage is the sum of CURRENT object sizes in the database (version storage is
// not counted, matching the previous check-then-write quota logic). A plain check-then-write lets
// concurrent uploads each pass the check and together overshoot the quota, so
// writers take an in-process reservation: the bytes a write intends to (or
// has so far) put into the bucket are counted against the quota until its
// metadata row is committed and the reservation released. Reservations grow
// with the bytes actually read, so a chunked upload that understates its size
// is cut off once it would exceed the quota, and an authoritative re-check
// against fresh database usage runs before the final byte is handed to the
// backend (see guardedBody).
//
// Remaining limitations: reservations are per process, so replicas behind a
// load balancer can still overshoot by up to one in-flight upload each; an
// overwrite is counted as additive (the replaced object's size is not
// subtracted), which is conservative.

type quotaExceededError struct {
	used  int64
	quota int64
}

func (e *quotaExceededError) Error() string {
	return fmt.Sprintf("bucket quota exceeded (%d of %d bytes used)", e.used, e.quota)
}

var quotaReservations = struct {
	sync.Mutex
	byBucket map[uuid.UUID]int64
}{byBucket: map[uuid.UUID]int64{}}

// bucketUsageFn returns the bucket's committed usage; a variable so tests can
// run without a database.
var bucketUsageFn = func(bucketID uuid.UUID) (int64, error) {
	var used int64
	err := database.DB.Model(&models.Object{}).
		Where("bucket_id = ?", bucketID).
		Select("COALESCE(SUM(size), 0)").Scan(&used).Error
	return used, err
}

// quotaReservation is one writer's claim on a bucket's quota. A nil
// reservation (bucket without quota) is valid and every method is a no-op.
type quotaReservation struct {
	bucketID uuid.UUID
	quota    int64
	base     int64 // committed usage when the reservation was taken
	held     int64
	released bool
}

// reserveBucketQuota claims size bytes (0 when unknown; the reservation grows
// as bytes are read) of the bucket's quota. Callers must release() once the
// write's metadata is committed or the write failed.
func reserveBucketQuota(bucket *models.Bucket, size int64) (*quotaReservation, error) {
	if bucket == nil || bucket.QuotaBytes <= 0 {
		return nil, nil
	}
	if size < 0 {
		size = 0
	}
	used, err := bucketUsageFn(bucket.ID)
	if err != nil {
		// Fail open on a transient DB error (fail open, as before).
		logger.Warn("Quota usage lookup failed; allowing write", map[string]interface{}{
			"bucket": bucket.Name, "error": err.Error(),
		})
		return nil, nil
	}
	quotaReservations.Lock()
	defer quotaReservations.Unlock()
	reserved := quotaReservations.byBucket[bucket.ID]
	if used+reserved+size > bucket.QuotaBytes {
		return nil, &quotaExceededError{used: used + reserved, quota: bucket.QuotaBytes}
	}
	quotaReservations.byBucket[bucket.ID] = reserved + size
	return &quotaReservation{bucketID: bucket.ID, quota: bucket.QuotaBytes, base: used, held: size}, nil
}

// ensure grows the reservation to total bytes, failing when that would exceed
// the quota (checked against the usage snapshot plus all live reservations).
func (r *quotaReservation) ensure(total int64) error {
	if r == nil || total <= r.held {
		return nil
	}
	quotaReservations.Lock()
	defer quotaReservations.Unlock()
	if r.released {
		return fmt.Errorf("quota reservation already released")
	}
	reserved := quotaReservations.byBucket[r.bucketID]
	grow := total - r.held
	if r.base+reserved+grow > r.quota {
		return &quotaExceededError{used: r.base + reserved - r.held, quota: r.quota}
	}
	quotaReservations.byBucket[r.bucketID] = reserved + grow
	r.held = total
	return nil
}

// finalize is the authoritative check once the final size is known: fresh
// committed usage + other writers' reservations + total must fit.
func (r *quotaReservation) finalize(total int64) error {
	if r == nil {
		return nil
	}
	used, err := bucketUsageFn(r.bucketID)
	if err != nil {
		used = r.base // fall back to the snapshot rather than failing the write
	}
	quotaReservations.Lock()
	defer quotaReservations.Unlock()
	if r.released {
		return fmt.Errorf("quota reservation already released")
	}
	reserved := quotaReservations.byBucket[r.bucketID]
	others := reserved - r.held
	if used+others+total > r.quota {
		return &quotaExceededError{used: used + others, quota: r.quota}
	}
	quotaReservations.byBucket[r.bucketID] = others + total
	r.held = total
	return nil
}

// release returns the reservation. Call it after the object's metadata row is
// committed (the usage is then visible in the database) or the write failed.
func (r *quotaReservation) release() {
	if r == nil {
		return
	}
	quotaReservations.Lock()
	defer quotaReservations.Unlock()
	if r.released {
		return
	}
	r.released = true
	left := quotaReservations.byBucket[r.bucketID] - r.held
	if left <= 0 {
		delete(quotaReservations.byBucket, r.bucketID)
	} else {
		quotaReservations.byBucket[r.bucketID] = left
	}
}
