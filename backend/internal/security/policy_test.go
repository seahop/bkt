package security

import (
	"strings"
	"testing"
)

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"*", "anything", true},
		{"abc", "abc", true},
		{"abc", "abd", false},
		{"abc*", "abcdef", true},
		{"abc*", "abXdef", false},
		{"*def", "abcdef", true},
		{"a*c", "abc", true},
		{"a*c", "ac", true},
		{"a*c", "abcc", true},
		{"a*c", "abd", false},
		{"s3:*", "s3:GetObject", true},
		{"s3:*", "s4:GetObject", false},
		{"bucket/*", "bucket/key", true},
		{"bucket/*", "bucket2/key", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.s); got != c.want {
			t.Errorf("globMatch(%q,%q)=%v want %v", c.pattern, c.s, got, c.want)
		}
	}
}

func TestMatchesActionCaseInsensitiveAndWildcards(t *testing.T) {
	cases := []struct {
		patterns []string
		action   string
		want     bool
	}{
		{[]string{"s3:GetObject"}, "s3:GetObject", true},
		{[]string{"s3:getobject"}, "s3:GetObject", true}, // case-insensitive
		{[]string{"S3:GETOBJECT"}, "s3:GetObject", true},
		{[]string{"s3:*"}, "s3:GetObject", true},
		{[]string{"s3:Get*"}, "s3:GetObject", true}, // partial wildcard
		{[]string{"s3:Get*"}, "s3:PutObject", false},
		{[]string{"*"}, "s3:GetObject", true},
		{[]string{"s3:PutObject"}, "s3:GetObject", false},
	}
	for _, c := range cases {
		if got := matchesAction(c.patterns, c.action); got != c.want {
			t.Errorf("matchesAction(%v,%q)=%v want %v", c.patterns, c.action, got, c.want)
		}
	}
}

func TestMatchesResourceCaseSensitiveAndPrefixGuard(t *testing.T) {
	cases := []struct {
		patterns []string
		resource string
		want     bool
	}{
		{[]string{"arn:aws:s3:::bucket/*"}, "arn:aws:s3:::bucket/key", true},
		{[]string{"arn:aws:s3:::bucket/*"}, "arn:aws:s3:::bucket-2/key", false}, // prefix guard
		{[]string{"arn:aws:s3:::bucket/photos/*"}, "arn:aws:s3:::bucket/photos/a.jpg", true},
		{[]string{"arn:aws:s3:::bucket/photos*"}, "arn:aws:s3:::bucket/photos/a.jpg", true}, // partial
		{[]string{"arn:aws:s3:::bucket/Key"}, "arn:aws:s3:::bucket/key", false},             // case-SENSITIVE
		{[]string{"*"}, "arn:aws:s3:::bucket/key", true},
	}
	for _, c := range cases {
		if got := matchesResource(c.patterns, c.resource); got != c.want {
			t.Errorf("matchesResource(%v,%q)=%v want %v", c.patterns, c.resource, got, c.want)
		}
	}
}

func TestMatchesPrincipal(t *testing.T) {
	cases := []struct {
		principal interface{}
		user      string
		want      bool
	}{
		{nil, "alice", true}, // absent → applies to all
		{"*", "alice", true},
		{"alice", "alice", true},
		{"alice", "bob", false},
		{[]interface{}{"alice", "bob"}, "bob", true},
		{[]interface{}{"alice"}, "bob", false},
		{map[string]interface{}{"AWS": "x"}, "alice", false},
		{map[string]interface{}{"AWS": "alice"}, "alice", true},
		{map[string]interface{}{"AWS": "*"}, "alice", true},
		{map[string]interface{}{"AWS": []interface{}{"bob", "arn:aws:iam::123456789012:user/alice"}}, "alice", true},
		{map[string]interface{}{"AWS": []interface{}{"arn:aws:iam::123456789012:user/team/alice"}}, "alice", true},
		{map[string]interface{}{"AWS": "arn:aws:iam::123456789012:root"}, "alice", false},
		{map[string]interface{}{"AWS": "arn:aws:iam::123456789012:user/*"}, "alice", false},
		{map[string]interface{}{"Service": "alice"}, "alice", false},
		{map[string]interface{}{"AWS": map[string]interface{}{"AWS": "alice"}}, "alice", false}, // unrecognized → fail closed
		{42.0, "alice", false},
		{"", "", false},
	}
	for _, c := range cases {
		if got := matchesPrincipal(c.principal, &PolicyEvaluationContext{Username: c.user}); got != c.want {
			t.Errorf("matchesPrincipal(%v,%q)=%v want %v", c.principal, c.user, got, c.want)
		}
	}
}

// An anonymous (unsigned public-read) requester has no username: only an
// absent Principal or "*" applies to it — never a named or empty principal.
func TestMatchesPrincipalAnonymous(t *testing.T) {
	anon := &PolicyEvaluationContext{Anonymous: true}
	cases := []struct {
		principal interface{}
		want      bool
	}{
		{nil, true},
		{"*", true},
		{"", false},
		{"alice", false},
		{[]interface{}{"alice", "*"}, true},
		{[]interface{}{"", "alice"}, false},
		{map[string]interface{}{"AWS": "*"}, true},
		{map[string]interface{}{"AWS": []interface{}{"*"}}, true},
		{map[string]interface{}{"AWS": "alice"}, false},
		{map[string]interface{}{"AWS": "arn:aws:iam::1:user/alice"}, false},
	}
	for _, c := range cases {
		if got := matchesPrincipal(c.principal, anon); got != c.want {
			t.Errorf("anonymous matchesPrincipal(%v)=%v want %v", c.principal, got, c.want)
		}
	}
}

func TestEvaluatePolicyAnonymousDeny(t *testing.T) {
	doc, err := ParseStoredPolicyDocument(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":"*","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::b/*"]},
		{"Effect":"Deny","Principal":"*","Action":["s3:GetObject"],"Resource":["arn:aws:s3:::b/secret/*"]},
		{"Effect":"Deny","Principal":["alice"],"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::b/alice-hidden/*"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	eval := func(res string) PolicyResult {
		return EvaluatePolicy(doc, &PolicyEvaluationContext{Anonymous: true, Action: "s3:GetObject", Resource: res})
	}
	if got := eval("arn:aws:s3:::b/secret/x"); got != PolicyDeny {
		t.Errorf("Principal * Deny must apply to anonymous, got %v", got)
	}
	if got := eval("arn:aws:s3:::b/alice-hidden/x"); got == PolicyDeny {
		t.Error("a Deny scoped to a named user must not apply to anonymous")
	}
	if got := eval("arn:aws:s3:::b/public.txt"); got != PolicyAllow {
		t.Errorf("unrelated key: got %v, want Allow (callers ignore Allow for anonymous)", got)
	}
}

func TestEvaluatePolicyDenyPrecedenceWithinDoc(t *testing.T) {
	doc := &PolicyDocument{
		Version: "2012-10-17",
		Statement: []PolicyStatement{
			{Effect: "Allow", Action: []string{"s3:*"}, Resource: []string{"*"}},
			{Effect: "Deny", Action: []string{"s3:DeleteObject"}, Resource: []string{"*"}},
		},
	}
	if got := EvaluatePolicy(doc, &PolicyEvaluationContext{Action: "s3:DeleteObject", Resource: "arn:aws:s3:::b/k"}); got != PolicyDeny {
		t.Errorf("expected PolicyDeny, got %v", got)
	}
	if got := EvaluatePolicy(doc, &PolicyEvaluationContext{Action: "s3:GetObject", Resource: "arn:aws:s3:::b/k"}); got != PolicyAllow {
		t.Errorf("expected PolicyAllow, got %v", got)
	}
}

func TestEvaluatePolicyAdminBypass(t *testing.T) {
	doc := GetDefaultDenyAllPolicy()
	ctx := &PolicyEvaluationContext{Action: "s3:GetObject", Resource: "*", IsAdmin: true}
	if got := EvaluatePolicy(doc, ctx); got != PolicyAllow {
		t.Errorf("admin should bypass deny-all, got %v", got)
	}
}

func TestEvaluatePolicyPrincipalScoping(t *testing.T) {
	doc := &PolicyDocument{
		Version: "2012-10-17",
		Statement: []PolicyStatement{
			{Effect: "Allow", Principal: "alice", Action: []string{"s3:GetObject"}, Resource: []string{"*"}},
		},
	}
	if got := EvaluatePolicy(doc, &PolicyEvaluationContext{Username: "alice", Action: "s3:GetObject", Resource: "arn:aws:s3:::b/k"}); got != PolicyAllow {
		t.Errorf("alice should match principal, got %v", got)
	}
	if got := EvaluatePolicy(doc, &PolicyEvaluationContext{Username: "bob", Action: "s3:GetObject", Resource: "arn:aws:s3:::b/k"}); got != PolicyNoMatch {
		t.Errorf("bob should not match principal (NoMatch), got %v", got)
	}
}

func TestEvaluatePolicyNoMatchDefault(t *testing.T) {
	doc := GetDefaultReadOnlyPolicy()
	ctx := &PolicyEvaluationContext{Action: "s3:DeleteObject", Resource: "arn:aws:s3:::b/k"}
	if got := EvaluatePolicy(doc, ctx); got != PolicyNoMatch {
		t.Errorf("expected NoMatch for unlisted action, got %v", got)
	}
}

func TestValidatePrincipal(t *testing.T) {
	good := []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":["s3:GetObject"],"Resource":["*"]}]}`,
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":["alice","bob"],"Action":["s3:GetObject"],"Resource":["*"]}]}`,
	}
	for _, g := range good {
		if _, err := ValidatePolicyDocument(g); err != nil {
			t.Errorf("expected valid, got error: %v (%s)", err, g)
		}
	}
	// AWS object form: "*", usernames and IAM user ARNs.
	for _, p := range []string{
		`{"AWS":"*"}`, `{"AWS":["*"]}`, `{"AWS":"alice"}`,
		`{"AWS":["alice","arn:aws:iam::123456789012:user/bob","arn:aws:iam::123456789012:user/path/carol"]}`,
		`"arn:aws:iam::123456789012:user/alice"`,
	} {
		doc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":` + p + `,"Action":["s3:GetObject"],"Resource":["*"]}]}`
		if _, err := ValidatePolicyDocument(doc); err != nil {
			t.Errorf("Principal %s: expected valid, got %v", p, err)
		}
	}
	// Other principal types / non-user ARNs are rejected with the supported forms.
	for _, p := range []string{
		`{"Service":"s3.amazonaws.com"}`, `{"CanonicalUser":"abc"}`, `{"AWS":"*","Federated":"x"}`, `{}`,
		`{"AWS":"arn:aws:iam::123456789012:root"}`, `{"AWS":"arn:aws:iam::123456789012:role/r"}`,
		`{"AWS":"arn:aws:iam::123456789012:user/*"}`, `{"AWS":{"AWS":"x"}}`, `{"AWS":[1]}`, `42`,
	} {
		doc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":` + p + `,"Action":["s3:GetObject"],"Resource":["*"]}]}`
		_, err := ValidatePolicyDocument(doc)
		if err == nil || !strings.Contains(err.Error(), "supported forms") {
			t.Errorf("Principal %s: expected an error listing the supported forms, got %v", p, err)
		}
	}
}

// AWS allows Action/Resource as a single string; bkt treats it as a
// one-element array.
func TestValidatePolicyDocumentStringActionResource(t *testing.T) {
	doc, err := ValidatePolicyDocument(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":{"AWS":"*"},"Action":"s3:GetObject","Resource":"arn:aws:s3:::b/secret/*"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Statement[0].Action) != 1 || doc.Statement[0].Resource[0] != "arn:aws:s3:::b/secret/*" {
		t.Fatalf("parsed %+v", doc.Statement[0])
	}
	ctx := &PolicyEvaluationContext{Username: "alice", Action: "s3:GetObject", Resource: "arn:aws:s3:::b/secret/x"}
	if got := EvaluatePolicy(doc, ctx); got != PolicyDeny {
		t.Errorf("string-form Deny: %v", got)
	}
	if _, err := ValidatePolicyDocument(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":1,"Resource":"*"}]}`); err == nil ||
		!strings.Contains(err.Error(), "string or an array") {
		t.Errorf("numeric Action: %v", err)
	}
}

func TestValidatePolicyDocumentRejectsUnsupportedElements(t *testing.T) {
	bad := map[string]string{
		"condition":               `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:ListBucket"],"Resource":["arn:aws:s3:::b"],"Condition":{"StringLike":{"s3:prefix":["home/alice/*"]}}}]}`,
		"condition lowercase key": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:ListBucket"],"Resource":["*"],"condition":{"Bool":{"aws:SecureTransport":"true"}}}]}`,
		"NotAction":               `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","NotAction":["s3:DeleteObject"],"Action":["s3:GetObject"],"Resource":["*"]}]}`,
		"NotResource":             `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["*"],"NotResource":["arn:aws:s3:::secret/*"]}]}`,
		"NotPrincipal":            `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","NotPrincipal":"bob","Action":["s3:GetObject"],"Resource":["*"]}]}`,
		"unknown statement field": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["*"],"Foo":1}]}`,
		"unknown top-level field": `{"Version":"2012-10-17","Bar":true,"Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["*"]}]}`,
		"trailing data":           `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["*"]}]} {}`,
	}
	for name, doc := range bad {
		if _, err := ValidatePolicyDocument(doc); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
	_, err := ValidatePolicyDocument(bad["condition"])
	if err == nil || !strings.Contains(err.Error(), "Condition is not supported yet") {
		t.Errorf("expected a clear Condition error, got %v", err)
	}
}

func TestValidatePolicyDocumentAcceptsSupportedForms(t *testing.T) {
	good := []string{
		// Frontend policy editor / template shapes.
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject","s3:ListBucket"],"Resource":["arn:aws:s3:::*","arn:aws:s3:::*/*"]}]}`,
		`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":["s3:*"],"Resource":["arn:aws:s3:::b","arn:aws:s3:::b/*"]}]}`,
		// Optional AWS elements that bkt accepts.
		`{"Version":"2012-10-17","Id":"doc-1","Statement":[{"Sid":"S1","Effect":"Allow","Principal":["alice"],"Action":["s3:GetObject"],"Resource":["*"]}]}`,
		// An empty Condition block constrains nothing and is harmless.
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":["*"],"Condition":{}}]}`,
		// Field names match case-insensitively (encoding/json semantics).
		`{"version":"2012-10-17","statement":[{"effect":"Allow","action":["s3:GetObject"],"resource":["*"]}]}`,
	}
	for _, g := range good {
		if _, err := ValidatePolicyDocument(g); err != nil {
			t.Errorf("expected valid, got %v (%s)", err, g)
		}
	}
}

func TestParseStoredPolicyDocumentIsLenient(t *testing.T) {
	docs := []string{
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:ListBucket"],"Resource":["*"],"Condition":{"StringLike":{"s3:prefix":["a/*"]}}}]}`,
		`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","NotAction":["s3:GetObject"],"Resource":["*"]}]}`,
		`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":["s3:*"],"NotResource":["arn:aws:s3:::public/*"]}]}`,
	}
	for _, d := range docs {
		if _, err := ParseStoredPolicyDocument(d); err != nil {
			t.Errorf("stored document should parse: %v (%s)", err, d)
		}
	}
}

func mustParseStored(t *testing.T, doc string) *PolicyDocument {
	t.Helper()
	p, err := ParseStoredPolicyDocument(doc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return p
}

func TestEvaluatePolicyFailSafeOnUnsupportedElements(t *testing.T) {
	ctx := func(action, resource string) *PolicyEvaluationContext {
		return &PolicyEvaluationContext{Username: "alice", Action: action, Resource: resource}
	}

	// Allow + Condition must NOT grant (the condition might have narrowed it).
	allowCond := mustParseStored(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*"],"Resource":["*"],"Condition":{"StringLike":{"s3:prefix":["home/alice/*"]}}}]}`)
	if got := EvaluatePolicy(allowCond, ctx("s3:GetObject", "arn:aws:s3:::b/other/k")); got != PolicyNoMatch {
		t.Errorf("Allow with Condition must not grant, got %v", got)
	}

	// Deny + Condition still denies (conservatively, as if unconditional).
	denyCond := mustParseStored(t, `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Action":["s3:*"],"Resource":["*"]},
		{"Effect":"Deny","Action":["s3:DeleteObject"],"Resource":["*"],"Condition":{"Bool":{"aws:MultiFactorAuthPresent":"false"}}}]}`)
	if got := EvaluatePolicy(denyCond, ctx("s3:DeleteObject", "arn:aws:s3:::b/k")); got != PolicyDeny {
		t.Errorf("Deny with Condition must deny, got %v", got)
	}
	if got := EvaluatePolicy(denyCond, ctx("s3:GetObject", "arn:aws:s3:::b/k")); got != PolicyAllow {
		t.Errorf("unrelated action should still be allowed, got %v", got)
	}

	// Allow + NotResource must not grant.
	allowNotRes := mustParseStored(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"NotResource":["arn:aws:s3:::secret/*"]}]}`)
	if got := EvaluatePolicy(allowNotRes, ctx("s3:GetObject", "arn:aws:s3:::public/k")); got != PolicyNoMatch {
		t.Errorf("Allow with NotResource must not grant, got %v", got)
	}

	// Deny + NotAction denies everything (including the excepted action).
	denyNotAction := mustParseStored(t, `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Action":["s3:*"],"Resource":["*"]},
		{"Effect":"Deny","NotAction":["s3:GetObject"],"Resource":["arn:aws:s3:::b/*"]}]}`)
	for _, a := range []string{"s3:PutObject", "s3:GetObject"} {
		if got := EvaluatePolicy(denyNotAction, ctx(a, "arn:aws:s3:::b/k")); got != PolicyDeny {
			t.Errorf("Deny with NotAction should deny %s conservatively, got %v", a, got)
		}
	}
	// ...but its Resource still scopes it.
	if got := EvaluatePolicy(denyNotAction, ctx("s3:PutObject", "arn:aws:s3:::other/k")); got != PolicyAllow {
		t.Errorf("Deny scoped to b/* should not affect other bucket, got %v", got)
	}

	// Deny + NotPrincipal applies to everyone.
	denyNotPrincipal := mustParseStored(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","NotPrincipal":"alice","Action":["s3:*"],"Resource":["*"]}]}`)
	if got := EvaluatePolicy(denyNotPrincipal, ctx("s3:GetObject", "arn:aws:s3:::b/k")); got != PolicyDeny {
		t.Errorf("Deny with NotPrincipal should apply to everyone, got %v", got)
	}
}
