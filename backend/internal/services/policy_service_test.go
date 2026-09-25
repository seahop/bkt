package services

import (
	"testing"

	"bkt/internal/security"
)

// bucketConfigActions are the bucket-configuration actions that gate the
// settings/versioning/lifecycle/replication endpoints.
var bucketConfigActions = []string{
	ActionPutBucketVersioning,
	ActionGetLifecycleConfiguration,
	ActionPutLifecycleConfiguration,
	ActionPutReplicationConfiguration,
	ActionPutBucketNotification,
	ActionPutBucketObjectLockConfiguration,
	ActionPutBucketQuota,
}

func TestBucketConfigActionsCoveredByWildcard(t *testing.T) {
	doc, err := security.ValidatePolicyDocument(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*"],"Resource":["arn:aws:s3:::b"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range bucketConfigActions {
		got := security.EvaluatePolicy(doc, &security.PolicyEvaluationContext{Action: a, Resource: "arn:aws:s3:::b"})
		if got != security.PolicyAllow {
			t.Errorf("s3:* should cover %s, got %v", a, got)
		}
	}
}

func TestBucketConfigActionsNotGrantedByObjectAccess(t *testing.T) {
	// A typical read/write data policy must not confer bucket-configuration rights.
	doc, err := security.ValidatePolicyDocument(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject","s3:ListBucket"],"Resource":["arn:aws:s3:::b","arn:aws:s3:::b/*"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range bucketConfigActions {
		got := security.EvaluatePolicy(doc, &security.PolicyEvaluationContext{Action: a, Resource: "arn:aws:s3:::b"})
		if got != security.PolicyNoMatch {
			t.Errorf("object-data policy must not grant %s, got %v", a, got)
		}
	}
}

func TestEvaluateStoredPolicyWithConditionFailSafe(t *testing.T) {
	ps := NewPolicyService()
	// Legacy stored document (strict validation would now reject it): the
	// conditional Allow must not grant, the conditional Deny must still deny.
	doc := `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["*"],"Condition":{"StringLike":{"s3:prefix":["x/*"]}}},
		{"Effect":"Deny","Action":["s3:DeleteObject"],"Resource":["*"],"Condition":{"Bool":{"aws:SecureTransport":"false"}}}]}`
	r, err := ps.evaluatePolicy(doc, ActionGetObject, "arn:aws:s3:::b/k", false, "alice")
	if err != nil || r != security.PolicyNoMatch {
		t.Errorf("conditional Allow: got %v, %v; want NoMatch", r, err)
	}
	r, err = ps.evaluatePolicy(doc, ActionDeleteObject, "arn:aws:s3:::b/k", false, "alice")
	if err != nil || r != security.PolicyDeny {
		t.Errorf("conditional Deny: got %v, %v; want Deny", r, err)
	}
}
