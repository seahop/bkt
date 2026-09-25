package validation

import (
	"strings"
	"testing"
)

func TestValidateObjectKeyAcceptsCanonicalKeys(t *testing.T) {
	good := []string{
		"a",
		"file.txt",
		"photos/2024/a.jpg",
		"folder/",           // S3 folder-marker object
		"folder/.keep",      // console folder placeholder
		".hidden",           // dot-files are fine
		"a/.b/c",            // dot-prefixed segments are fine
		"a/b.c/d",           // dots inside segments are fine
		".bkt-versions",     // only the prefix WITH the slash is reserved
		".bkt-versionsX/a",  // not the reserved prefix
		"x/.bkt-versions/y", // reserved only at the start of the key
		"name with spaces+plus%percent?q",
	}
	for _, k := range good {
		if err := ValidateObjectKey(k); err != nil {
			t.Errorf("ValidateObjectKey(%q) = %v, want nil", k, err)
		}
	}
}

func TestValidateObjectKeyRejectsAliasesAndTraversal(t *testing.T) {
	bad := []string{
		"",
		"/abs",
		"a//b",  // empty segment: aliases a/b on the filesystem
		"./x",   // aliases x
		"a/./b", // aliases a/b
		"a/.",   // aliases a
		".",
		"a//", // empty segment before the trailing slash
		"//",  // empty
		"a/../b",
		"..",
		"a\\b",
		"a\x00b",
		strings.Repeat("k", 1025),
	}
	for _, k := range bad {
		if err := ValidateObjectKey(k); err == nil {
			t.Errorf("ValidateObjectKey(%q) = nil, want error", k)
		}
	}
}

func TestValidateObjectKeyRejectsReservedVersionKeyspace(t *testing.T) {
	for _, k := range []string{
		".bkt-versions/",
		".bkt-versions/a",
		".bkt-versions/a/0b0c3b51-0000-4000-8000-000000000000",
	} {
		if err := ValidateObjectKey(k); err == nil {
			t.Errorf("ValidateObjectKey(%q) = nil, want reserved-key error", k)
		}
		if !IsReservedObjectKey(k) {
			t.Errorf("IsReservedObjectKey(%q) = false, want true", k)
		}
	}
	if IsReservedObjectKey("docs/.bkt-versions/a") {
		t.Error("reserved prefix must only match at the start of the key")
	}
}

func TestEscapeLikeWildcards(t *testing.T) {
	cases := map[string]string{
		"a_b":   `a\_b`,
		"50%":   `50\%`,
		`a\b`:   `a\\b`,
		"plain": "plain",
	}
	for in, want := range cases {
		if got := EscapeLikeWildcards(in); got != want {
			t.Errorf("EscapeLikeWildcards(%q) = %q, want %q", in, got, want)
		}
	}
}
