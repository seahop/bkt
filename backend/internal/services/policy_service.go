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
func (ps *PolicyService) CheckBucketAccess(userID uuid.UUID, bucketName, action string) (result bool, err error) {
	// Recover from panics to prevent service crash (fail-safe: deny access on panic)
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("bucket access check panic: %v", r)
			result = false
		}
	}()

	// Get user with policies
	var user models.User
	if err := database.DB.Preload("Policies").First(&user, userID).Error; err != nil {
		return false, fmt.Errorf("failed to fetch user: %w", err)
	}

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

	// Check user policies
	userPolicyResult := ps.evaluateUserPolicies(&user, action, resourceARN)

	// Get bucket policy if it exists
	var bucketPolicy models.BucketPolicy
	hasBucketPolicy := database.DB.Where("bucket_id = ?", bucket.ID).First(&bucketPolicy).Error == nil

	if hasBucketPolicy {
		// Evaluate bucket policy
		bucketPolicyResult, err := ps.evaluateBucketPolicy(&bucketPolicy, action, resourceARN)
		if err != nil {
			// If bucket policy is malformed, fall back to user policies only
			return userPolicyResult, nil
		}

		// Combine results: explicit deny wins, then explicit allow
		// If either policy explicitly denies, deny
		// If either policy explicitly allows (and no deny), allow
		if bucketPolicyResult == false && userPolicyResult == false {
			return false, nil // Both deny or neither allow
		}
		return bucketPolicyResult || userPolicyResult, nil
	}

	// No bucket policy - use user policies only
	return userPolicyResult, nil
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
	var user models.User
	if err := database.DB.Preload("Policies").First(&user, userID).Error; err != nil {
		return false, fmt.Errorf("failed to fetch user: %w", err)
	}

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

	// Check user policies
	userPolicyResult := ps.evaluateUserPolicies(&user, action, resourceARN)

	// Get bucket policy if it exists
	var bucketPolicy models.BucketPolicy
	hasBucketPolicy := database.DB.Where("bucket_id = ?", bucket.ID).First(&bucketPolicy).Error == nil

	if hasBucketPolicy {
		// Evaluate bucket policy
		bucketPolicyResult, err := ps.evaluateBucketPolicy(&bucketPolicy, action, resourceARN)
		if err != nil {
			// If bucket policy is malformed, fall back to user policies only
			return userPolicyResult, nil
		}

		// Combine results: explicit deny wins
		if bucketPolicyResult == false && userPolicyResult == false {
			return false, nil // Both deny or neither allow
		}
		return bucketPolicyResult || userPolicyResult, nil
	}

	// No bucket policy - use user policies only
	return userPolicyResult, nil
}

// evaluateUserPolicies evaluates all user policies
func (ps *PolicyService) evaluateUserPolicies(user *models.User, action, resource string) bool {
	// Admin bypass
	if user.IsAdmin {
		return true
	}

	// No policies attached - deny by default
	if len(user.Policies) == 0 {
		return false
	}

	hasExplicitDeny := false
	hasExplicitAllow := false

	// Evaluate each policy
	for _, policy := range user.Policies {
		result, err := ps.evaluatePolicy(policy.Document, action, resource, user.IsAdmin)
		if err != nil {
			// Skip malformed policies
			continue
		}

		if result == false {
			hasExplicitDeny = true
		} else if result == true {
			hasExplicitAllow = true
		}
	}

	// Deny overrides allow (MinIO approach)
	if hasExplicitDeny {
		return false
	}

	// Return allow if at least one policy allows
	return hasExplicitAllow
}

// evaluateBucketPolicy evaluates a bucket policy
func (ps *PolicyService) evaluateBucketPolicy(bucketPolicy *models.BucketPolicy, action, resource string) (bool, error) {
	return ps.evaluatePolicy(bucketPolicy.PolicyDocument, action, resource, false)
}

// evaluatePolicy parses and evaluates a policy document with panic recovery
func (ps *PolicyService) evaluatePolicy(policyJSON string, action, resource string, isAdmin bool) (result bool, err error) {
	// Recover from panics in policy evaluation (prevent resource leaks)
	defer func() {
		if r := recover(); r != nil {
			// Convert panic to error instead of crashing the service
			err = fmt.Errorf("policy evaluation panic: %v", r)
			result = false
		}
	}()

	// Parse and validate policy document
	policyDoc, err := security.ValidatePolicyDocument(policyJSON)
	if err != nil {
		return false, fmt.Errorf("failed to parse policy: %w", err)
	}

	// Create evaluation context
	ctx := &security.PolicyEvaluationContext{
		Action:   action,
		Resource: resource,
		IsAdmin:  isAdmin,
	}

	// Evaluate using the security package
	return security.EvaluatePolicy(policyDoc, ctx), nil
}

// GetUserPolicies retrieves all policies attached to a user
func (ps *PolicyService) GetUserPolicies(userID uuid.UUID) ([]models.Policy, error) {
	var user models.User
	if err := database.DB.Preload("Policies").First(&user, userID).Error; err != nil {
		return nil, fmt.Errorf("failed to fetch user: %w", err)
	}
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
	var user models.User
	if err := database.DB.Preload("Policies").First(&user, userID).Error; err != nil {
		return nil, fmt.Errorf("failed to fetch user: %w", err)
	}

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
		// Build resource ARN
		resourceARN := fmt.Sprintf("arn:aws:s3:::%s", bucket.Name)

		// Check user policies
		userPolicyResult := ps.evaluateUserPolicies(&user, action, resourceARN)

		// Check bucket policy if exists
		bucketPolicy, hasBucketPolicy := bucketPolicyMap[bucket.ID]
		if hasBucketPolicy {
			bucketPolicyResult, err := ps.evaluateBucketPolicy(bucketPolicy, action, resourceARN)
			if err != nil {
				// If bucket policy is malformed, fall back to user policies only
				if userPolicyResult {
					accessibleBuckets = append(accessibleBuckets, bucket)
				}
				continue
			}

			// Combine results: explicit deny wins, then explicit allow
			if bucketPolicyResult || userPolicyResult {
				accessibleBuckets = append(accessibleBuckets, bucket)
			}
		} else {
			// No bucket policy - use user policies only
			if userPolicyResult {
				accessibleBuckets = append(accessibleBuckets, bucket)
			}
		}
	}

	return accessibleBuckets, nil
}
