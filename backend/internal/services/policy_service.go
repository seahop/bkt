package services

import (
	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/security"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
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

// Bucket-configuration actions. These gate the bucket settings endpoints
// (versioning, lifecycle, replication, webhook notifications, WORM retention,
// quota) for non-admins; bucket ownership alone grants nothing. AWS names are
// used where AWS has an equivalent; s3:PutBucketQuota is a bkt extension.
// All are covered by "s3:*" (and e.g. "s3:Put*").
const (
	ActionPutBucketVersioning              = "s3:PutBucketVersioning"
	ActionGetLifecycleConfiguration        = "s3:GetLifecycleConfiguration"
	ActionPutLifecycleConfiguration        = "s3:PutLifecycleConfiguration" // also covers deleting the configuration, as in AWS
	ActionPutReplicationConfiguration      = "s3:PutReplicationConfiguration"
	ActionPutBucketNotification            = "s3:PutBucketNotification"            // webhook URL/secret/events
	ActionPutBucketObjectLockConfiguration = "s3:PutBucketObjectLockConfiguration" // WORM retention_days
	ActionPutBucketQuota                   = "s3:PutBucketQuota"                   // bkt extension: quota_bytes
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

func (ps *PolicyService) CheckBucketAccess(userID uuid.UUID, bucketName, action string) (bool, error) {
	allowed, err := ps.CheckBucketActions(userID, bucketName, []string{action})
	return allowed[action], err
}

// CheckBucketActions is CheckBucketAccess for several bucket-level actions at
// once: the user, their policies, the bucket and its policy are loaded once.
// The result maps each action to whether it is allowed; on error every action
// is denied.
func (ps *PolicyService) CheckBucketActions(userID uuid.UUID, bucketName string, actions []string) (result map[string]bool, err error) {
	result = make(map[string]bool, len(actions))
	// Recover from panics to prevent service crash (fail-safe: deny access on panic)
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("bucket access check panic: %v", r)
			result = map[string]bool{}
		}
	}()

	// Get user with policies
	userPtr, err := loadUserWithEffectivePolicies(userID)
	if err != nil {
		return result, fmt.Errorf("failed to fetch user: %w", err)
	}
	user := *userPtr

	// Admin bypass - admins can do anything
	if user.IsAdmin {
		for _, a := range actions {
			result[a] = true
		}
		return result, nil
	}

	// Get bucket (to check ownership and bucket policies)
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		// Bucket doesn't exist - deny access
		return result, nil
	}

	// Build resource ARN
	resourceARN := fmt.Sprintf("arn:aws:s3:::%s", bucketName)

	var bucketPolicy *models.BucketPolicy
	var bp models.BucketPolicy
	if database.DB.Where("bucket_id = ?", bucket.ID).First(&bp).Error == nil {
		bucketPolicy = &bp
	}

	// Evaluate user (identity) and bucket (resource) policies, then combine so an
	// explicit Deny from either source wins.
	for _, action := range actions {
		userResult := ps.evaluateUserPolicies(&user, action, resourceARN)
		bucketResult := security.PolicyNoMatch
		if bucketPolicy != nil {
			if br, perr := ps.evaluateBucketPolicy(bucketPolicy, action, resourceARN, user.Username); perr == nil {
				bucketResult = br
			}
		}
		result[action] = decide(userResult, bucketResult)
	}
	return result, nil
}

// CheckObjectAccess checks if a user has permission to perform an action on an
// object. On a public-read bucket (is_public) an object read (s3:GetObject,
// and bkt's s3:HeadObject) that no policy explicitly denies is allowed even
// without an Allow, as in AWS where public-read applies to every principal
// (see objectPolicies.allowed for the evaluation order).
func (ps *PolicyService) CheckObjectAccess(userID uuid.UUID, bucketName, objectKey, action string) (bool, error) {
	return ps.checkObjectAccess(userID, bucketName, objectKey, action, true)
}

// CheckObjectAccessWithoutPublicRead is CheckObjectAccess without the
// public-read grant: only policies decide. Use it for reads that public-read
// does not cover even though bkt authorizes them as s3:GetObject — a specific
// (versionId-addressed) or listed object version, and object tagging (AWS
// s3:GetObjectVersion, s3:GetObjectTagging).
func (ps *PolicyService) CheckObjectAccessWithoutPublicRead(userID uuid.UUID, bucketName, objectKey, action string) (bool, error) {
	return ps.checkObjectAccess(userID, bucketName, objectKey, action, false)
}

func (ps *PolicyService) checkObjectAccess(userID uuid.UUID, bucketName, objectKey, action string, honorPublicRead bool) (result bool, err error) {
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

	// The bucket row (loaded above, including is_public) and its policy are
	// all the public-read rule needs. A bucket policy that cannot be loaded
	// (other than "none") may hold a Deny, so public-read then does not apply.
	var bucketPolicy *models.BucketPolicy
	var bp models.BucketPolicy
	publicRead := bucket.IsPublic && honorPublicRead
	if perr := database.DB.Where("bucket_id = ?", bucket.ID).First(&bp).Error; perr == nil {
		bucketPolicy = &bp
	} else if !errors.Is(perr, gorm.ErrRecordNotFound) {
		publicRead = false
	}
	return ps.objectAccessDecision(&user, bucketName, publicRead, bucketPolicy, objectKey, action), nil
}

// objectAccessDecision is the in-memory core of CheckObjectAccess for a user
// whose bucket exists: it applies objectPolicies.allowed to the user's
// effective (direct + group) policies and the bucket policy (nil = none).
// publicRead is whether the bucket's public-read grant applies (bucket
// is_public, and the caller honors it). AccessEvaluator shares the same core
// (see TestAccessEvaluatorMatchesCheckObjectAccess).
func (ps *PolicyService) objectAccessDecision(user *models.User, bucketName string, publicRead bool, bucketPolicy *models.BucketPolicy, objectKey, action string) bool {
	if user.IsAdmin {
		return true
	}
	p := parseObjectPolicies(user, bucketPolicy, publicRead)
	return p.allowed(bucketName, objectKey, action)
}

// objectPolicies is the parsed policy set that authorizes one (non-admin)
// user's object requests in one bucket: the user's effective policies, the
// bucket policy, and whether the bucket is public-read.
type objectPolicies struct {
	username  string
	userDocs  []*security.PolicyDocument
	bucketDoc *security.PolicyDocument
	public    bool // the bucket's public-read grant applies
	// unparseable is set when a stored document failed to parse. Such a
	// document is skipped for Allow/Deny (as before), but public-read — a
	// grant that relies on seeing every Deny — then does not apply.
	unparseable bool
}

// parseObjectPolicies parses every document once.
func parseObjectPolicies(user *models.User, bucketPolicy *models.BucketPolicy, public bool) objectPolicies {
	p := objectPolicies{username: user.Username, public: public}
	for _, pol := range user.Policies {
		doc, err := security.ParseStoredPolicyDocument(pol.Document)
		if err != nil {
			p.unparseable = true
			continue
		}
		p.userDocs = append(p.userDocs, doc)
	}
	if bucketPolicy != nil {
		doc, err := security.ParseStoredPolicyDocument(bucketPolicy.PolicyDocument)
		if err != nil {
			p.unparseable = true
		} else {
			p.bucketDoc = doc
		}
	}
	return p
}

// result is the combined tri-state result of the user's policies and the
// bucket policy (bucket-policy Principals match the username): an explicit
// Deny from any of them wins, else Allow if any allows, else NoMatch. A
// document whose evaluation panics is skipped.
func (p *objectPolicies) result(action, resource string) security.PolicyResult {
	ctx := &security.PolicyEvaluationContext{Username: p.username, Action: action, Resource: resource}
	userResult := security.PolicyNoMatch
	for _, doc := range p.userDocs {
		r, ok := safeEvaluate(doc, ctx)
		if !ok {
			continue
		}
		if r == security.PolicyDeny {
			userResult = security.PolicyDeny
			break
		}
		if r == security.PolicyAllow {
			userResult = security.PolicyAllow
		}
	}
	bucketResult := security.PolicyNoMatch
	if p.bucketDoc != nil {
		if r, ok := safeEvaluate(p.bucketDoc, ctx); ok {
			bucketResult = r
		}
	}
	return combine(userResult, bucketResult)
}

// allowed decides action on bucketName/key, in this order:
//
//  1. an explicit Deny in any applicable policy (user, group, or a bucket
//     policy statement whose Principal names the user, is "*", or is absent)
//     denies;
//  2. otherwise an Allow in any of them allows;
//  3. otherwise, when public-read applies (p.public), an object read —
//     s3:GetObject, or s3:HeadObject provided s3:GetObject is not explicitly
//     denied either (AWS authorizes HEAD as s3:GetObject) — is allowed:
//     public-read applies to every principal, as in AWS;
//  4. otherwise it is denied (implicit deny).
//
// Public-read never grants listing, versions, writes, deletes, tagging or
// any other action. Admins are handled by the callers (always allowed).
func (p *objectPolicies) allowed(bucketName, key, action string) bool {
	resource := fmt.Sprintf("arn:aws:s3:::%s/%s", bucketName, key)
	switch p.result(action, resource) {
	case security.PolicyDeny:
		return false
	case security.PolicyAllow:
		return true
	}
	if !p.public || p.unparseable || !isPublicReadAction(action) {
		return false
	}
	if !strings.EqualFold(action, ActionGetObject) && p.result(ActionGetObject, resource) == security.PolicyDeny {
		return false
	}
	return true
}

// isPublicReadAction reports whether action is an object read that a
// public-read bucket grants to every principal.
func isPublicReadAction(action string) bool {
	return strings.EqualFold(action, ActionGetObject) || strings.EqualFold(action, ActionHeadObject)
}

// AnonymousObjectAccessDenied reports whether the bucket policy explicitly
// denies an anonymous (unsigned, public-read) request for action on the
// object. For anonymous requests the bucket's is_public flag is the only
// grant: Allow statements and user/group policies are irrelevant, but a Deny
// statement with no Principal or Principal "*" still applies. It fails
// closed: a bucket policy that cannot be loaded or parsed denies.
func (ps *PolicyService) AnonymousObjectAccessDenied(bucket *models.Bucket, objectKey, action string) (denied bool) {
	defer func() {
		if r := recover(); r != nil {
			denied = true
		}
	}()
	var bp models.BucketPolicy
	if err := database.DB.Where("bucket_id = ?", bucket.ID).First(&bp).Error; err != nil {
		return !errors.Is(err, gorm.ErrRecordNotFound)
	}
	return anonymousDenied(bp.PolicyDocument, bucket.Name, objectKey, action)
}

// anonymousDenied is the in-memory core of AnonymousObjectAccessDenied.
func anonymousDenied(policyJSON, bucketName, objectKey, action string) bool {
	doc, err := security.ParseStoredPolicyDocument(policyJSON)
	if err != nil {
		return true
	}
	return security.EvaluatePolicy(doc, &security.PolicyEvaluationContext{
		Anonymous: true,
		Action:    action,
		Resource:  fmt.Sprintf("arn:aws:s3:::%s/%s", bucketName, objectKey),
	}) == security.PolicyDeny
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

	// Stored documents are parsed leniently: a legacy document containing an
	// element that strict validation now rejects (e.g. Condition) is still
	// evaluated — fail-safe, by security.EvaluatePolicy — instead of being
	// skipped wholesale, which would also discard its Deny statements.
	policyDoc, err := security.ParseStoredPolicyDocument(policyJSON)
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
	return combine(userResult, bucketResult) == security.PolicyAllow
}

// combine is decide's tri-state form: Deny if either source denies, else
// Allow if either allows, else NoMatch (implicit deny).
func combine(userResult, bucketResult security.PolicyResult) security.PolicyResult {
	switch {
	case userResult == security.PolicyDeny || bucketResult == security.PolicyDeny:
		return security.PolicyDeny
	case userResult == security.PolicyAllow || bucketResult == security.PolicyAllow:
		return security.PolicyAllow
	default:
		return security.PolicyNoMatch
	}
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
