package services

import (
	"fmt"
	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/security"

	"github.com/google/uuid"
)

// S3 Actions - Standard AWS S3 action constants
const (
	ActionListAllMyBuckets  = "s3:ListAllMyBuckets"
	ActionGetBucketLocation = "s3:GetBucketLocation"
	ActionCreateBucket      = "s3:CreateBucket"
	ActionDeleteBucket      = "s3:DeleteBucket"
	ActionListBucket        = "s3:ListBucket"
	ActionGetObject         = "s3:GetObject"
	ActionPutObject         = "s3:PutObject"
	ActionDeleteObject      = "s3:DeleteObject"
	ActionHeadObject        = "s3:HeadObject"
	ActionGetBucketPolicy   = "s3:GetBucketPolicy"
	ActionPutBucketPolicy   = "s3:PutBucketPolicy"
)

// PolicyService handles policy evaluation and enforcement
type PolicyService struct{}

// NewPolicyService creates a new policy service
func NewPolicyService() *PolicyService {
	return &PolicyService{}
}

// CheckBucketAccess checks if a user has permission to perform an action on a bucket

// loadUserWithEffectivePolicies loads a user with their EFFECTIVE policies:
// directly-attached policies plus the policies of every group they belong to
// (deduplicated). All authorization checks must resolve policies through this
// so group membership actually grants access.
func loadUserWithEffectivePolicies(userID uuid.UUID) (*models.User, error) {
	var user models.User
	if err := database.DB.Preload("Policies").First(&user, userID).Error; err != nil {
		return nil, err
	}
	var groupPolicies []models.Policy
	if err := database.DB.
		Joins("JOIN group_policies gp ON gp.policy_id = policies.id").
		Joins("JOIN user_groups ug ON ug.group_id = gp.group_id").
		Where("ug.user_id = ?", userID).
		Find(&groupPolicies).Error; err != nil {
		return nil, err
	}
	seen := make(map[uuid.UUID]bool, len(user.Policies))
	for _, p := range user.Policies {
		seen[p.ID] = true
	}
	for _, p := range groupPolicies {
		if !seen[p.ID] {
			user.Policies = append(user.Policies, p)
			seen[p.ID] = true
		}
	}
	return &user, nil
}

func (ps *PolicyService) CheckBucketAccess(userID uuid.UUID, bucketName, action string) (result bool, err error) {
	// Recover from panics to prevent service crash (fail-safe: deny access on panic)
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("bucket access check panic: %v", r)
			result = false
		}
	}()

	// Get user with policies
	userPtr, err := loadUserWithEffectivePolicies(userID)
	if err != nil {
		return false, fmt.Errorf("failed to fetch user: %w", err)
	}
	user := *userPtr

	// Admin bypass - admins can do anything
	if user.IsAdmin {
		return true, nil
	}

	// Get bucket (to check ownership and bucket policies)
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		// Bucket doesn't exist - deny access
		return false, nil
	}

	// Build resource ARN
	resourceARN := fmt.Sprintf("arn:aws:s3:::%s", bucketName)

	// Evaluate user (identity) and bucket (resource) policies, then combine so an
	// explicit Deny from either source wins.
	userResult := ps.evaluateUserPolicies(&user, action, resourceARN)

	bucketResult := security.PolicyNoMatch
	var bucketPolicy models.BucketPolicy
	if database.DB.Where("bucket_id = ?", bucket.ID).First(&bucketPolicy).Error == nil {
		if br, perr := ps.evaluateBucketPolicy(&bucketPolicy, action, resourceARN, user.Username); perr == nil {
			bucketResult = br
		}
	}

	return decide(userResult, bucketResult), nil
}

// CheckObjectAccess checks if a user has permission to perform an action on an object
func (ps *PolicyService) CheckObjectAccess(userID uuid.UUID, bucketName, objectKey, action string) (result bool, err error) {
	// Recover from panics to prevent service crash (fail-safe: deny access on panic)
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("object access check panic: %v", r)
			result = false
		}
	}()

	// Get user with policies
	userPtr, err := loadUserWithEffectivePolicies(userID)
	if err != nil {
		return false, fmt.Errorf("failed to fetch user: %w", err)
	}
	user := *userPtr

	// Admin bypass - admins can do anything
	if user.IsAdmin {
		return true, nil
	}

	// Get bucket (to check bucket policies)
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		// Bucket doesn't exist - deny access
		return false, nil
	}

	// Build resource ARN - for objects, include the key
	resourceARN := fmt.Sprintf("arn:aws:s3:::%s/%s", bucketName, objectKey)

	// Evaluate user (identity) and bucket (resource) policies, then combine so an
	// explicit Deny from either source wins.
	userResult := ps.evaluateUserPolicies(&user, action, resourceARN)

	bucketResult := security.PolicyNoMatch
	var bucketPolicy models.BucketPolicy
	if database.DB.Where("bucket_id = ?", bucket.ID).First(&bucketPolicy).Error == nil {
		if br, perr := ps.evaluateBucketPolicy(&bucketPolicy, action, resourceARN, user.Username); perr == nil {
			bucketResult = br
		}
	}

	return decide(userResult, bucketResult), nil
}

// evaluateUserPolicies evaluates all attached user policies and returns a
// tri-state result. Explicit Deny anywhere wins; otherwise Allow if any policy
// allows; otherwise NoMatch. Returning the tri-state (rather than a bool) lets
// callers honor an explicit user Deny even when a bucket policy allows.
func (ps *PolicyService) evaluateUserPolicies(user *models.User, action, resource string) security.PolicyResult {
	if user.IsAdmin {
		return security.PolicyAllow
	}
	if len(user.Policies) == 0 {
		return security.PolicyNoMatch
	}

	result := security.PolicyNoMatch
	for _, policy := range user.Policies {
		r, err := ps.evaluatePolicy(policy.Document, action, resource, user.IsAdmin, user.Username)
		if err != nil {
			continue // skip malformed policies
		}
		switch r {
		case security.PolicyDeny:
			return security.PolicyDeny // explicit deny wins immediately
		case security.PolicyAllow:
			result = security.PolicyAllow
		}
	}
	return result
}

// evaluateBucketPolicy evaluates a bucket policy returning a tri-state result.
// The requesting username is supplied so the policy's Principal can scope it.
func (ps *PolicyService) evaluateBucketPolicy(bucketPolicy *models.BucketPolicy, action, resource, username string) (security.PolicyResult, error) {
	return ps.evaluatePolicy(bucketPolicy.PolicyDocument, action, resource, false, username)
}

// evaluatePolicy parses and evaluates a policy document with panic recovery.
func (ps *PolicyService) evaluatePolicy(policyJSON string, action, resource string, isAdmin bool, username string) (result security.PolicyResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("policy evaluation panic: %v", r)
			result = security.PolicyNoMatch
		}
	}()

	policyDoc, err := security.ValidatePolicyDocument(policyJSON)
	if err != nil {
		return security.PolicyNoMatch, fmt.Errorf("failed to parse policy: %w", err)
	}

	ctx := &security.PolicyEvaluationContext{
		Username: username,
		Action:   action,
		Resource: resource,
		IsAdmin:  isAdmin,
	}

	return security.EvaluatePolicy(policyDoc, ctx), nil
}

// decide combines a user-policy result with a bucket-policy result into a final
// allow/deny. An explicit Deny from EITHER source wins (matching IAM semantics);
// otherwise access is granted if EITHER source allows.
func decide(userResult, bucketResult security.PolicyResult) bool {
	if userResult == security.PolicyDeny || bucketResult == security.PolicyDeny {
		return false
	}
	return userResult == security.PolicyAllow || bucketResult == security.PolicyAllow
}

// GetUserPolicies retrieves all policies attached to a user
func (ps *PolicyService) GetUserPolicies(userID uuid.UUID) ([]models.Policy, error) {
	userPtr, err := loadUserWithEffectivePolicies(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch user: %w", err)
	}
	user := *userPtr
	return user.Policies, nil
}

// GetBucketPolicy retrieves the policy document for a bucket
func (ps *PolicyService) GetBucketPolicy(bucketName string) (*models.BucketPolicy, error) {
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		return nil, fmt.Errorf("bucket not found: %w", err)
	}

	var bucketPolicy models.BucketPolicy
	if err := database.DB.Where("bucket_id = ?", bucket.ID).First(&bucketPolicy).Error; err != nil {
		return nil, fmt.Errorf("bucket policy not found: %w", err)
	}

	return &bucketPolicy, nil
}

// SetBucketPolicy sets or updates the policy document for a bucket
func (ps *PolicyService) SetBucketPolicy(bucketName, policyDocument string) error {
	// Validate policy document first
	if _, err := security.ValidatePolicyDocument(policyDocument); err != nil {
		return fmt.Errorf("invalid policy document: %w", err)
	}

	// Get bucket
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		return fmt.Errorf("bucket not found: %w", err)
	}

	// Check if bucket policy already exists
	var bucketPolicy models.BucketPolicy
	err := database.DB.Where("bucket_id = ?", bucket.ID).First(&bucketPolicy).Error

	if err != nil {
		// Create new bucket policy
		bucketPolicy = models.BucketPolicy{
			BucketID:       bucket.ID,
			PolicyDocument: policyDocument,
		}
		return database.DB.Create(&bucketPolicy).Error
	}

	// Update existing policy
	bucketPolicy.PolicyDocument = policyDocument
	return database.DB.Save(&bucketPolicy).Error
}

// DeleteBucketPolicy removes the policy document from a bucket
func (ps *PolicyService) DeleteBucketPolicy(bucketName string) error {
	// Get bucket
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		return fmt.Errorf("bucket not found: %w", err)
	}

	// Delete bucket policy
	return database.DB.Where("bucket_id = ?", bucket.ID).Delete(&models.BucketPolicy{}).Error
}

// FilterAccessibleBuckets performs batch permission checks on a list of buckets
// Returns only buckets the user has permission to access (fixes N+1 query problem)
func (ps *PolicyService) FilterAccessibleBuckets(userID uuid.UUID, buckets []models.Bucket, action string) ([]models.Bucket, error) {
	// Empty list - return early
	if len(buckets) == 0 {
		return buckets, nil
	}

	// Load user with policies ONCE (instead of N times)
	userPtr, err := loadUserWithEffectivePolicies(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch user: %w", err)
	}
	user := *userPtr

	// Admin bypass - admins can access all buckets
	if user.IsAdmin {
		return buckets, nil
	}

	// Collect all bucket IDs for batch loading
	bucketIDs := make([]uuid.UUID, len(buckets))
	bucketIDMap := make(map[uuid.UUID]*models.Bucket)
	for i := range buckets {
		bucketIDs[i] = buckets[i].ID
		bucketIDMap[buckets[i].ID] = &buckets[i]
	}

	// Load all bucket policies in ONE query (instead of N queries)
	var bucketPolicies []models.BucketPolicy
	database.DB.Where("bucket_id IN ?", bucketIDs).Find(&bucketPolicies)

	// Create map of bucket ID to policy for fast lookup
	bucketPolicyMap := make(map[uuid.UUID]*models.BucketPolicy)
	for i := range bucketPolicies {
		bucketPolicyMap[bucketPolicies[i].BucketID] = &bucketPolicies[i]
	}

	// Filter buckets - evaluate permissions in memory
	accessibleBuckets := make([]models.Bucket, 0, len(buckets))
	for _, bucket := range buckets {
		resourceARN := fmt.Sprintf("arn:aws:s3:::%s", bucket.Name)

		userResult := ps.evaluateUserPolicies(&user, action, resourceARN)

		bucketResult := security.PolicyNoMatch
		if bucketPolicy, ok := bucketPolicyMap[bucket.ID]; ok {
			if br, perr := ps.evaluateBucketPolicy(bucketPolicy, action, resourceARN, user.Username); perr == nil {
				bucketResult = br
			}
		}

		if decide(userResult, bucketResult) {
			accessibleBuckets = append(accessibleBuckets, bucket)
		}
	}

	return accessibleBuckets, nil
}
