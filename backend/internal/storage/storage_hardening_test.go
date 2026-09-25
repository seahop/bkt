package storage

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"bkt/internal/validation"
)

func TestLocalRejectsAliasingKeys(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	data := []byte("secret")
	if err := ls.PutObject("b", "dir/file", bytes.NewReader(data), int64(len(data)), "", nil); err != nil {
		t.Fatal(err)
	}
	// Every one of these would resolve (filepath.Join cleans) onto dir/file or
	// dir; none may be usable to read, write or delete it.
	aliases := []string{"dir//file", "./dir/file", "dir/./file", "dir/file/", "dir/", "dir/."}
	for _, k := range aliases {
		if _, err := ls.GetObject("b", k); err == nil {
			t.Errorf("GetObject(%q) succeeded, want rejection", k)
		}
		if err := ls.PutObject("b", k, bytes.NewReader([]byte("x")), 1, "", nil); err == nil {
			t.Errorf("PutObject(%q) succeeded, want rejection", k)
		}
		if err := ls.DeleteObject("b", k); err == nil {
			t.Errorf("DeleteObject(%q) succeeded, want rejection", k)
		}
		if err := ls.CopyObject("b", "dir/file", k); err == nil {
			t.Errorf("CopyObject(-> %q) succeeded, want rejection", k)
		}
		if _, err := ls.CreateMultipartUpload("b", k, "", nil); err == nil {
			t.Errorf("CreateMultipartUpload(%q) succeeded, want rejection", k)
		}
	}
	rc, err := ls.GetObject("b", "dir/file")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if got, _ := io.ReadAll(rc); !bytes.Equal(got, data) {
		t.Errorf("canonical object modified through an alias: %q", got)
	}
}

func TestLocalArchiveNeverOverwritesExistingVersion(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	const vid = "0b0c3b51-0000-4000-8000-000000000001"

	put := func(s string) {
		t.Helper()
		if err := ls.PutObject("b", "k", bytes.NewReader([]byte(s)), int64(len(s)), "", nil); err != nil {
			t.Fatal(err)
		}
	}
	put("v1")
	if err := ls.ArchiveObjectVersion("b", "k", vid); err != nil {
		t.Fatalf("first archive: %v", err)
	}
	// A second archive under the same version id (e.g. two racing overwrites
	// that both read the same current row) must fail rather than destroy v1.
	put("v2")
	if err := ls.ArchiveObjectVersion("b", "k", vid); err == nil {
		t.Fatal("second archive under the same version id succeeded; archived bytes were overwritten")
	}
	rc, err := ls.GetObjectVersion("b", "k", vid)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if got, _ := io.ReadAll(rc); string(got) != "v1" {
		t.Errorf("archived version = %q, want v1", got)
	}
	// The current object is untouched by the failed archive.
	cur, err := ls.GetObject("b", "k")
	if err != nil {
		t.Fatalf("current object lost after refused archive: %v", err)
	}
	defer cur.Close()
	if got, _ := io.ReadAll(cur); string(got) != "v2" {
		t.Errorf("current = %q, want v2", got)
	}
}

func TestLocalVersionStorageUnreachableFromKeys(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	for _, k := range []string{"../.versions/b/k", "../.multipart/x"} {
		if err := ls.PutObject("b", k, bytes.NewReader([]byte("x")), 1, "", nil); err == nil {
			t.Errorf("PutObject(%q) reached internal storage", k)
		}
	}
	if err := ls.PutObject(".versions", "b/k", bytes.NewReader([]byte("x")), 1, "", nil); err == nil {
		t.Error("a bucket named .versions must not be addressable")
	}
	if _, err := os.Stat(filepath.Join(ls.rootPath, ".versions")); !os.IsNotExist(err) {
		t.Error("version storage was created by a user write")
	}
}

func TestGetObjectRangeLocalAndFallback(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	data := []byte("0123456789abcdef")
	_ = ls.PutObject("b", "k", bytes.NewReader(data), int64(len(data)), "", nil)

	rc, err := GetObjectRange(ls, "b", "k", 4, 5)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "45678" {
		t.Errorf("native range = %q, want 45678", got)
	}

	// A backend without RangeReader falls back to GetObject + skip.
	rc, err = GetObjectRange(noRange{ls}, "b", "k", 10, 100)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = io.ReadAll(rc)
	_ = rc.Close()
	if string(got) != "abcdef" {
		t.Errorf("fallback range = %q, want abcdef", got)
	}
}

// noRange hides LocalStorage's RangeReader implementation.
type noRange struct{ StorageBackend }

func TestLocalPartSizes(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	id, err := ls.CreateMultipartUpload("b", "k", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = ls.UploadPart("b", "k", id, 1, bytes.NewReader(make([]byte, 10)), 10)
	_, _ = ls.UploadPart("b", "k", id, 3, bytes.NewReader(make([]byte, 7)), 7)
	sizes, err := PartSizes(ls, "b", "k", id)
	if err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 2 || sizes[1] != 10 || sizes[3] != 7 {
		t.Errorf("PartSizes = %v", sizes)
	}
}

func TestS3ReservedPrefixMatchesValidation(t *testing.T) {
	if s3VersionPrefix != validation.ReservedObjectKeyPrefix {
		t.Fatalf("storage version prefix %q != validation reserved prefix %q", s3VersionPrefix, validation.ReservedObjectKeyPrefix)
	}
	if err := checkS3UserKey(".bkt-versions/a/b"); err == nil {
		t.Error("checkS3UserKey accepted a reserved key")
	}
	if err := checkS3UserKey("docs/a"); err != nil {
		t.Errorf("checkS3UserKey rejected a normal key: %v", err)
	}
	if _, err := s3VersionKey(".bkt-versions/x", "0b0c3b51-0000-4000-8000-000000000001"); err == nil {
		t.Error("s3VersionKey accepted a key inside the version keyspace")
	}
}

func TestS3CopySourceEncoding(t *testing.T) {
	cases := map[string]string{
		"plain/key.txt":        "bkt/plain/key.txt",
		"a b+c%d?e#f&g=h":      "bkt/a%20b%2Bc%25d%3Fe%23f%26g%3Dh",
		"dir/ünï.txt":          "bkt/dir/%C3%BCn%C3%AF.txt",
		"keep-_.~chars":        "bkt/keep-_.~chars",
		"folder/":              "bkt/folder/",
		"colon:semi;comma,at@": "bkt/colon%3Asemi%3Bcomma%2Cat%40",
	}
	for key, want := range cases {
		if got := s3CopySource("bkt", key); got != want {
			t.Errorf("s3CopySource(%q) = %q, want %q", key, got, want)
		}
	}
}
