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
func (ps *PolicyService) CheckBucketAccess(userID uuid.UUID, bucketName, action string) (bool, error) {
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
func (ps *PolicyService) CheckObjectAccess(userID uuid.UUID, bucketName, objectKey, action string) (bool, error) {
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

// evaluatePolicy parses and evaluates a policy document
func (ps *PolicyService) evaluatePolicy(policyJSON string, action, resource string, isAdmin bool) (bool, error) {
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
