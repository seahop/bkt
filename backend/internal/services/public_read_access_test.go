package services

import (
	"testing"

	"bkt/internal/models"
)

// TestObjectAccessPublicRead covers the public-read rule of the shared object
// decision core (objectAccessDecision / AccessEvaluator.Allowed): explicit
// Deny > Allow > public-read for object reads > implicit deny.
func TestObjectAccessPublicRead(t *testing.T) {
	const bucket = "pub"
	doc := func(effect, principal, action, resource string) string {
		p := ""
		if principal != "" {
			p = `"Principal":` + principal + `,`
		}
		return `{"Version":"2012-10-17","Statement":[{"Effect":"` + effect + `",` + p +
			`"Action":["` + action + `"],"Resource":["` + resource + `"]}]}`
	}
	denyKeyGet := doc("Deny", "", "s3:GetObject", "arn:aws:s3:::pub/secret/*")
	denyAllKey := doc("Deny", "", "s3:*", "arn:aws:s3:::pub/secret/*")
	denyPut := doc("Deny", "", "s3:PutObject", "arn:aws:s3:::pub/*")
	allowList := doc("Allow", "", "s3:ListBucket", "arn:aws:s3:::pub/*")
	bpDenyStar := &models.BucketPolicy{PolicyDocument: doc("Deny", `"*"`, "s3:GetObject", "arn:aws:s3:::pub/secret/*")}
	bpDenyNoPrincipal := &models.BucketPolicy{PolicyDocument: doc("Deny", "", "s3:GetObject", "arn:aws:s3:::pub/secret/*")}
	bpDenyAlice := &models.BucketPolicy{PolicyDocument: doc("Deny", `["alice"]`, "s3:GetObject", "arn:aws:s3:::pub/secret/*")}
	bpDenyBob := &models.BucketPolicy{PolicyDocument: doc("Deny", `"bob"`, "s3:GetObject", "arn:aws:s3:::pub/secret/*")}
	bpMalformed := &models.BucketPolicy{PolicyDocument: `{not json`}

	alice := func(docs ...string) models.User {
		u := models.User{Username: "alice"}
		for _, d := range docs {
			u.Policies = append(u.Policies, pol(d))
		}
		return u
	}
	type check struct {
		action, key string
		want        bool
	}
	cases := []struct {
		name   string
		user   models.User
		public bool
		bp     *models.BucketPolicy
		checks []check
	}{
		{"no policy, public", alice(), true, nil, []check{
			{ActionGetObject, "a.txt", true},
			{ActionHeadObject, "a.txt", true},
			{ActionGetObject, "secret/k", true},
			{ActionListBucket, "a.txt", false},
			{ActionPutObject, "a.txt", false},
			{ActionDeleteObject, "a.txt", false},
			{"s3:GetObjectTagging", "a.txt", false},
			{"s3:GetObjectVersion", "a.txt", false},
		}},
		{"no policy, private", alice(), false, nil, []check{
			{ActionGetObject, "a.txt", false},
			{ActionHeadObject, "a.txt", false},
		}},
		{"user Deny GetObject on key", alice(denyKeyGet), true, nil, []check{
			{ActionGetObject, "secret/k", false},
			{ActionHeadObject, "secret/k", false}, // HEAD is a GetObject read
			{ActionGetObject, "a.txt", true},
		}},
		{"user Deny s3:* on key", alice(denyAllKey), true, nil, []check{
			{ActionGetObject, "secret/k", false},
			{ActionHeadObject, "secret/k", false},
			{ActionGetObject, "other", true},
		}},
		{"unrelated Deny does not block public read", alice(denyPut), true, nil, []check{
			{ActionGetObject, "a.txt", true},
			{ActionPutObject, "a.txt", false},
		}},
		{"unrelated Allow keeps public read", alice(allowList), true, nil, []check{
			{ActionGetObject, "a.txt", true},
			{ActionListBucket, "a.txt", true},
		}},
		// Group policies reach the decision core merged into the user's
		// effective policies (loadUserWithEffectivePolicies).
		{"group Deny (effective policy)", alice(allowList, denyKeyGet), true, nil, []check{
			{ActionGetObject, "secret/k", false},
			{ActionGetObject, "a.txt", true},
		}},
		{"bucket policy Deny Principal *", alice(), true, bpDenyStar, []check{
			{ActionGetObject, "secret/k", false},
			{ActionHeadObject, "secret/k", false},
			{ActionGetObject, "a.txt", true},
		}},
		{"bucket policy Deny without Principal", alice(), true, bpDenyNoPrincipal, []check{
			{ActionGetObject, "secret/k", false},
			{ActionGetObject, "a.txt", true},
		}},
		{"bucket policy Deny naming the user", alice(), true, bpDenyAlice, []check{
			{ActionGetObject, "secret/k", false},
			{ActionGetObject, "a.txt", true},
		}},
		{"bucket policy Deny naming another user", alice(), true, bpDenyBob, []check{
			{ActionGetObject, "secret/k", true},
		}},
		{"unparseable bucket policy fails closed", alice(), true, bpMalformed, []check{
			{ActionGetObject, "a.txt", false},
		}},
		{"unparseable user policy fails closed", alice(`{not json`), true, nil, []check{
			{ActionGetObject, "a.txt", false},
		}},
		{"admin", models.User{Username: "root", IsAdmin: true, Policies: []models.Policy{pol(denyAllKey)}}, false, bpDenyStar, []check{
			{ActionGetObject, "secret/k", true},
			{ActionPutObject, "a.txt", true},
		}},
	}

	ps := NewPolicyService()
	for _, tc := range cases {
		ev := NewAccessEvaluatorFromData(&tc.user, bucket, &models.Bucket{Name: bucket, IsPublic: tc.public}, tc.bp)
		for _, c := range tc.checks {
			if got := ps.objectAccessDecision(&tc.user, bucket, tc.public, tc.bp, c.key, c.action); got != c.want {
				t.Errorf("%s: objectAccessDecision(%s, %s) = %v, want %v", tc.name, c.action, c.key, got, c.want)
			}
			if got := ev.Allowed(c.action, c.key); got != c.want {
				t.Errorf("%s: AccessEvaluator.Allowed(%s, %s) = %v, want %v", tc.name, c.action, c.key, got, c.want)
			}
		}
	}
}
