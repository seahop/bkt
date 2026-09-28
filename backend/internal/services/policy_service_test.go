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

// Anonymous (public-read) requests: only an explicit Deny for Principal "*"
// or without a Principal applies; Allows are irrelevant and a policy that
// cannot be parsed fails closed.
func TestAnonymousDenied(t *testing.T) {
	doc := `{"Version":"2012-10-17","Statement":[
		{"Effect":"Deny","Principal":"*","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::pub/secret/*"]},
		{"Effect":"Deny","Action":["s3:Get*"],"Resource":["arn:aws:s3:::pub/private.txt"]},
		{"Effect":"Deny","Principal":["alice"],"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::pub/alice/*"]},
		{"Effect":"Allow","Principal":"*","Action":["s3:*"],"Resource":["*"]}]}`
	cases := []struct {
		key  string
		want bool
	}{
		{"secret/a", true},
		{"private.txt", true},
		{"alice/a", false},
		{"hello.txt", false},
	}
	for _, c := range cases {
		if got := anonymousDenied(doc, "pub", c.key, ActionGetObject); got != c.want {
			t.Errorf("anonymousDenied(%q) = %v, want %v", c.key, got, c.want)
		}
	}
	if !anonymousDenied(`not json`, "pub", "hello.txt", ActionGetObject) {
		t.Error("an unparseable bucket policy must fail closed")
	}
	// Legacy stored document with a Condition on a Deny: applied conservatively.
	legacy := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::pub/*"],"Condition":{"IpAddress":{"aws:SourceIp":"10.0.0.0/8"}}}]}`
	if !anonymousDenied(legacy, "pub", "hello.txt", ActionGetObject) {
		t.Error("a conditional Deny must apply conservatively")
	}
}
