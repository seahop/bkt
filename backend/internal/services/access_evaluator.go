package services

import (
	"errors"
	"fmt"

	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/security"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AccessEvaluator answers object-level authorization questions for ONE user on
// ONE bucket without touching the database per call. It loads the user
// (is_admin, is_locked), the user's effective (direct + group) policies and the
// bucket policy once, parses every document once, and then evaluates in memory
// with the same semantics as CheckObjectAccess:
//
//   - a missing or locked user is denied everything (CheckObjectAccess relies
//     on authentication to exclude locked users; background jobs acting on a
//     user's behalf have no such gate, so the evaluator enforces it itself);
//   - an admin is allowed everything;
//   - a missing bucket denies everything for non-admins;
//   - otherwise user and bucket policies are evaluated (bucket-policy
//     Principals match the username), an explicit Deny from either wins, and
//     on a public-read bucket an object read no policy denies is allowed
//     (objectPolicies.allowed, shared with CheckObjectAccess).
//
// It is intended for sweeps (replication, lifecycle) that check many keys.
// The snapshot is taken at construction; build a new evaluator per sweep.
type AccessEvaluator struct {
	bucketName  string
	userMissing bool
	userLocked  bool
	isAdmin     bool
	bucketFound bool
	policies    objectPolicies
}

// NewAccessEvaluator loads the data needed to authorize userID's actions on
// bucketName. A missing user or bucket is not an error (the evaluator then
// denies, as CheckObjectAccess would); only database failures are.
func (s *PolicyService) NewAccessEvaluator(userID uuid.UUID, bucketName string) (*AccessEvaluator, error) {
	user, err := loadUserWithEffectivePolicies(userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &AccessEvaluator{bucketName: bucketName, userMissing: true}, nil
		}
		return nil, fmt.Errorf("failed to fetch user: %w", err)
	}

	var bucketPolicy *models.BucketPolicy
	var found *models.Bucket
	if !user.IsAdmin && !user.IsLocked {
		var bucket models.Bucket
		err := database.DB.Where("name = ?", bucketName).First(&bucket).Error
		switch {
		case err == nil:
			found = &bucket
			var bp models.BucketPolicy
			perr := database.DB.Where("bucket_id = ?", bucket.ID).First(&bp).Error
			if perr == nil {
				bucketPolicy = &bp
			} else if !errors.Is(perr, gorm.ErrRecordNotFound) {
				return nil, fmt.Errorf("failed to fetch bucket policy: %w", perr)
			}
		case errors.Is(err, gorm.ErrRecordNotFound):
		default:
			return nil, fmt.Errorf("failed to fetch bucket: %w", err)
		}
	}
	return NewAccessEvaluatorFromData(user, bucketName, found, bucketPolicy), nil
}

// NewAccessEvaluatorFromData builds an evaluator from already-loaded data (no
// DB): the user with its effective policies, the bucket (nil = it does not
// exist; its is_public flag enables the public-read grant), and its bucket
// policy (nil = none). Documents that fail to parse are skipped, exactly as
// CheckObjectAccess skips them.
func NewAccessEvaluatorFromData(user *models.User, bucketName string, bucket *models.Bucket, bucketPolicy *models.BucketPolicy) *AccessEvaluator {
	return &AccessEvaluator{
		bucketName:  bucketName,
		userLocked:  user.IsLocked,
		isAdmin:     user.IsAdmin,
		bucketFound: bucket != nil,
		policies:    parseObjectPolicies(user, bucketPolicy, bucket != nil && bucket.IsPublic),
	}
}

// UserMissing reports whether the user no longer exists.
func (e *AccessEvaluator) UserMissing() bool { return e.userMissing }

// UserLocked reports whether the user's account is locked.
func (e *AccessEvaluator) UserLocked() bool { return e.userLocked }

// IsAdmin reports whether the user is (currently) an admin and not locked.
func (e *AccessEvaluator) IsAdmin() bool { return e.isAdmin && !e.userLocked && !e.userMissing }

// Allowed reports whether the user may perform action on key in the bucket.
func (e *AccessEvaluator) Allowed(action, key string) bool {
	if e == nil || e.userMissing || e.userLocked {
		return false
	}
	if e.isAdmin {
		return true
	}
	if !e.bucketFound {
		return false
	}
	return e.policies.allowed(e.bucketName, key, action)
}

// safeEvaluate runs security.EvaluatePolicy with the same panic recovery as
// evaluatePolicy (a panic yields "no result", i.e. the document is skipped).
func safeEvaluate(doc *security.PolicyDocument, ctx *security.PolicyEvaluationContext) (r security.PolicyResult, ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			r, ok = security.PolicyNoMatch, false
		}
	}()
	return security.EvaluatePolicy(doc, ctx), true
}
