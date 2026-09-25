package auth

import (
	"encoding/json"
	"testing"
)

func TestScrubPrincipal(t *testing.T) {
	doc := `{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":"alice","Action":["s3:GetObject"],"Resource":["b/*"]},
		{"Effect":"Allow","Principal":["alice","bob"],"Action":["s3:PutObject"],"Resource":["b/*"]},
		{"Effect":"Allow","Principal":"*","Action":["s3:ListBucket"],"Resource":["b"]},
		{"Effect":"Deny","Action":["s3:DeleteObject"],"Resource":["b/*"]},
		{"Effect":"Allow","Principal":{"AWS":["alice"]},"Action":["s3:GetObject"],"Resource":["b/x"]}
	]}`
	out, modified, empty, err := scrubPrincipal(doc, "alice")
	if err != nil || !modified || empty {
		t.Fatalf("modified=%v empty=%v err=%v", modified, empty, err)
	}
	var p struct {
		Statement []map[string]interface{}
	}
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatal(err)
	}
	// alice-only statements are dropped (never widened to everyone), the
	// shared one keeps bob, unrelated ones are untouched.
	if len(p.Statement) != 3 {
		t.Fatalf("statements = %d: %s", len(p.Statement), out)
	}
	if pr, _ := p.Statement[0]["Principal"].([]interface{}); len(pr) != 1 || pr[0] != "bob" {
		t.Errorf("shared principal = %v", p.Statement[0]["Principal"])
	}
	if p.Statement[1]["Principal"] != "*" {
		t.Errorf("wildcard statement changed: %v", p.Statement[1])
	}
	if _, has := p.Statement[2]["Principal"]; has {
		t.Errorf("principal-less statement changed: %v", p.Statement[2])
	}
	if policyNamesPrincipal(out, "alice") {
		t.Error("alice still referenced after scrub")
	}
	if !policyNamesPrincipal(doc, "alice") || policyNamesPrincipal(doc, "carol") {
		t.Error("policyNamesPrincipal mismatch")
	}

	// Only-alice policy becomes empty (caller deletes it).
	_, modified, empty, _ = scrubPrincipal(`{"Statement":{"Effect":"Allow","Principal":"alice","Action":["s3:*"],"Resource":["*"]}}`, "alice")
	if !modified || !empty {
		t.Errorf("single-statement policy: modified=%v empty=%v", modified, empty)
	}
	// Substring names are not matched.
	if _, modified, _, _ = scrubPrincipal(`{"Statement":[{"Effect":"Allow","Principal":["alice2"],"Action":["s3:*"],"Resource":["*"]}]}`, "alice"); modified {
		t.Error("alice2 must not match alice")
	}
}

func TestLikeEscape(t *testing.T) {
	if got := likeEscape(`a_b%c\d`); got != `a\_b\%c\\d` {
		t.Errorf("likeEscape = %q", got)
	}
}
