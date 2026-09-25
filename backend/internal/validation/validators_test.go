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

func TestValidateObjectKeyRejectsTraversal(t *testing.T) {
	bad := []string{
		"",
		"/abs",
		"//",
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
		if err := ValidateLocalObjectKey(k); err == nil {
			t.Errorf("ValidateLocalObjectKey(%q) = nil, want error", k)
		}
	}
}

// Non-canonical spellings are distinct keys on S3 (backend-independent
// validation accepts them) but alias on the filesystem (local rule rejects).
func TestNonCanonicalKeysAreLocalOnlyRejections(t *testing.T) {
	aliases := []string{
		"a//b",  // empty segment: aliases a/b on the filesystem
		"./x",   // aliases x
		"a/./b", // aliases a/b
		"a/.",   // aliases a
		".",
		"a//",    // empty segment before the trailing slash
		"a/b//",  // two trailing slashes
		"dir/./", // '.' segment in a marker key
	}
	for _, k := range aliases {
		if err := ValidateObjectKey(k); err != nil {
			t.Errorf("ValidateObjectKey(%q) = %v, want nil (valid S3 key)", k, err)
		}
		if err := ValidateLocalObjectKey(k); err == nil {
			t.Errorf("ValidateLocalObjectKey(%q) = nil, want error", k)
		}
	}
}

func TestValidateLocalObjectKeyFolderMarkers(t *testing.T) {
	for _, k := range []string{"dir/", "a/b/", "dir/file.txt", "dir/.keep", ".bkt-folderX", "x.bkt-folder"} {
		if err := ValidateLocalObjectKey(k); err != nil {
			t.Errorf("ValidateLocalObjectKey(%q) = %v, want nil", k, err)
		}
	}
	for _, k := range []string{LocalFolderMarkerName, "dir/" + LocalFolderMarkerName, LocalFolderMarkerName + "/x", "a/" + LocalFolderMarkerName + "/"} {
		if err := ValidateLocalObjectKey(k); err == nil {
			t.Errorf("ValidateLocalObjectKey(%q) = nil, want reserved-name error", k)
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
