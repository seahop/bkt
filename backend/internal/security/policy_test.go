package security

import "testing"

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
		{map[string]interface{}{"AWS": "x"}, "alice", false}, // unrecognized → fail closed
	}
	for _, c := range cases {
		if got := matchesPrincipal(c.principal, c.user); got != c.want {
			t.Errorf("matchesPrincipal(%v,%q)=%v want %v", c.principal, c.user, got, c.want)
		}
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
	// AWS object form is rejected (bkt principals are usernames)
	bad := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"x"},"Action":["s3:GetObject"],"Resource":["*"]}]}`
	if _, err := ValidatePolicyDocument(bad); err == nil {
		t.Errorf("expected error for object-form principal")
	}
}
