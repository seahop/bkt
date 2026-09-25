package services

import (
	"fmt"
	"testing"

	"bkt/internal/models"
)

func pol(doc string) models.Policy { return models.Policy{Document: doc} }

// TestAccessEvaluatorMatchesCheckObjectAccess proves the evaluator decides
// exactly like CheckObjectAccess's decision core for the same loaded data.
func TestAccessEvaluatorMatchesCheckObjectAccess(t *testing.T) {
	const bucket = "src"
	allowAll := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*"],"Resource":["arn:aws:s3:::src","arn:aws:s3:::src/*"]}]}`
	denySecret := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::src/secret/*"]}]}`
	denyOneKey := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":["s3:*"],"Resource":["arn:aws:s3:::src/x.txt"]}]}`
	readOnly := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:Get*"],"Resource":["arn:aws:s3:::src/*"]}]}`
	bpAllowAlice := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":["alice"],"Action":["s3:PutObject"],"Resource":["arn:aws:s3:::src/*"]}]}`
	bpDenyAlicePub := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"alice","Action":["s3:*"],"Resource":["arn:aws:s3:::src/pub/*"]}]}`
	bpAllowBob := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"bob","Action":["s3:*"],"Resource":["arn:aws:s3:::src/*"]}]}`
	legacyCond := `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["*"],"Condition":{"StringLike":{"s3:prefix":["x/*"]}}},
		{"Effect":"Deny","Action":["s3:DeleteObject"],"Resource":["*"],"Condition":{"Bool":{"aws:SecureTransport":"false"}}}]}`
	malformed := `{not json`

	type setup struct {
		name   string
		user   models.User
		bucket *models.BucketPolicy
	}
	setups := []setup{
		{"no policies", models.User{Username: "alice"}, nil},
		{"admin", models.User{Username: "root", IsAdmin: true, Policies: []models.Policy{pol(denyOneKey)}}, nil},
		{"allow all", models.User{Username: "alice", Policies: []models.Policy{pol(allowAll)}}, nil},
		{"allow all + deny prefix", models.User{Username: "alice", Policies: []models.Policy{pol(allowAll), pol(denySecret)}}, nil},
		{"deny first then allow", models.User{Username: "alice", Policies: []models.Policy{pol(denyOneKey), pol(allowAll)}}, nil},
		{"read only", models.User{Username: "alice", Policies: []models.Policy{pol(readOnly)}}, nil},
		{"bucket policy grants alice", models.User{Username: "alice", Policies: []models.Policy{pol(readOnly)}}, &models.BucketPolicy{PolicyDocument: bpAllowAlice}},
		{"bucket policy deny wins over user allow", models.User{Username: "alice", Policies: []models.Policy{pol(allowAll)}}, &models.BucketPolicy{PolicyDocument: bpDenyAlicePub}},
		{"bucket policy other principal", models.User{Username: "alice"}, &models.BucketPolicy{PolicyDocument: bpAllowBob}},
		{"legacy condition doc", models.User{Username: "alice", Policies: []models.Policy{pol(legacyCond), pol(allowAll)}}, nil},
		{"malformed skipped", models.User{Username: "alice", Policies: []models.Policy{pol(malformed), pol(readOnly)}}, &models.BucketPolicy{PolicyDocument: malformed}},
	}
	actions := []string{ActionGetObject, ActionPutObject, ActionDeleteObject, ActionListBucket}
	keys := []string{"a.txt", "x.txt", "secret/k", "pub/p", "x/y", "deep/secret/z"}

	ps := NewPolicyService()
	for _, s := range setups {
		ev := NewAccessEvaluatorFromData(&s.user, bucket, true, s.bucket)
		for _, a := range actions {
			for _, k := range keys {
				want := ps.objectAccessDecision(&s.user, bucket, s.bucket, k, a)
				if got := ev.Allowed(a, k); got != want {
					t.Errorf("%s: Allowed(%s,%s)=%v, CheckObjectAccess=%v", s.name, a, k, got, want)
				}
			}
		}
	}

	// Spot-check the key semantics explicitly (not just equivalence).
	u := models.User{Username: "alice", Policies: []models.Policy{pol(allowAll), pol(denySecret)}}
	ev := NewAccessEvaluatorFromData(&u, bucket, true, nil)
	if ev.Allowed(ActionGetObject, "secret/k") || !ev.Allowed(ActionGetObject, "a.txt") || !ev.Allowed(ActionPutObject, "secret/k") {
		t.Error("narrow Deny must deny only the matching key/action")
	}
}

func TestAccessEvaluatorDeniesLockedMissingAndNoBucket(t *testing.T) {
	allowAll := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*"],"Resource":["*"]}]}`
	locked := models.User{Username: "alice", IsLocked: true, Policies: []models.Policy{pol(allowAll)}}
	if NewAccessEvaluatorFromData(&locked, "b", true, nil).Allowed(ActionGetObject, "k") {
		t.Error("locked user must be denied")
	}
	lockedAdmin := models.User{Username: "root", IsAdmin: true, IsLocked: true}
	ev := NewAccessEvaluatorFromData(&lockedAdmin, "b", true, nil)
	if ev.Allowed(ActionGetObject, "k") || ev.IsAdmin() {
		t.Error("locked admin must be denied")
	}
	if (&AccessEvaluator{userMissing: true}).Allowed(ActionGetObject, "k") {
		t.Error("missing user must be denied")
	}
	var nilEv *AccessEvaluator
	if nilEv.Allowed(ActionGetObject, "k") {
		t.Error("nil evaluator must deny")
	}
	u := models.User{Username: "alice", Policies: []models.Policy{pol(allowAll)}}
	if NewAccessEvaluatorFromData(&u, "b", false, nil).Allowed(ActionGetObject, "k") {
		t.Error("missing bucket must deny non-admins")
	}
	admin := models.User{Username: "root", IsAdmin: true}
	if !NewAccessEvaluatorFromData(&admin, "b", false, nil).Allowed(ActionDeleteObject, "k") {
		t.Error("admin must be allowed")
	}
}

func BenchmarkAccessEvaluatorAllowed(b *testing.B) {
	u := models.User{Username: "alice", Policies: []models.Policy{pol(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*"],"Resource":["arn:aws:s3:::b/*"]}]}`)}}
	ev := NewAccessEvaluatorFromData(&u, "b", true, nil)
	for i := 0; i < b.N; i++ {
		ev.Allowed(ActionGetObject, fmt.Sprintf("k%d", i))
	}
}
