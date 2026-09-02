package storage

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestLocal(t *testing.T) *LocalStorage {
	t.Helper()
	dir := t.TempDir()
	return NewLocalStorage(dir)
}

func TestResolveRejectsTraversal(t *testing.T) {
	ls := newTestLocal(t)
	bad := []struct{ bucket, key string }{
		{"b", "../escape"},
		{"b", "../../etc/passwd"},
		{"..", "x"},
		{"b", "a/../../../../etc/passwd"},
	}
	for _, c := range bad {
		if _, err := ls.resolve(c.bucket, c.key); err == nil {
			t.Errorf("resolve(%q,%q) should have been rejected", c.bucket, c.key)
		}
	}
	// A normal nested key must be allowed and stay under the root.
	p, err := ls.resolve("b", "photos/2024/a.jpg")
	if err != nil {
		t.Fatalf("unexpected error for valid key: %v", err)
	}
	if !strings.HasPrefix(p, ls.rootPath) {
		t.Errorf("resolved path %q escaped root %q", p, ls.rootPath)
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	ls := newTestLocal(t)
	if err := ls.CreateBucket("b", ""); err != nil {
		t.Fatal(err)
	}
	data := []byte("hello world")
	if err := ls.PutObject("b", "dir/file.txt", bytes.NewReader(data), int64(len(data)), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	rc, err := ls.GetObject("b", "dir/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, data) {
		t.Errorf("round trip mismatch: got %q", got)
	}
}

func TestPutObjectAtomicNoTempLeak(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	data := []byte("payload")
	_ = ls.PutObject("b", "k", bytes.NewReader(data), int64(len(data)), "", nil)
	// No leftover temp files should appear in listings.
	objs, err := ls.ListObjects("b", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objs {
		if strings.Contains(o.Key, ".tmp-upload-") {
			t.Errorf("temp file leaked into listing: %s", o.Key)
		}
	}
	if len(objs) != 1 {
		t.Errorf("expected 1 object, got %d", len(objs))
	}
}

func TestSelfCopyPreservesData(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	data := []byte("keep me intact")
	_ = ls.PutObject("b", "k", bytes.NewReader(data), int64(len(data)), "", nil)

	// Copying an object onto itself must not truncate it.
	if err := ls.CopyObject("b", "k", "k"); err != nil {
		t.Fatalf("self-copy errored: %v", err)
	}
	rc, err := ls.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, data) {
		t.Errorf("self-copy corrupted data: got %q want %q", got, data)
	}
}

func TestMultipartUploadIDGuard(t *testing.T) {
	ls := newTestLocal(t)
	// A non-UUID uploadID must be rejected before any filesystem access, so a
	// crafted value can't drive AbortMultipartUpload's os.RemoveAll out of root.
	if _, err := ls.multipartDir("../../etc"); err == nil {
		t.Error("multipartDir accepted a non-UUID uploadID")
	}
	if err := ls.AbortMultipartUpload("b", "k", "../../etc"); err == nil {
		t.Error("AbortMultipartUpload accepted a non-UUID uploadID")
	}
}

func TestMultipartRoundTrip(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	uploadID, err := ls.CreateMultipartUpload("b", "big", "application/octet-stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	p1 := bytes.Repeat([]byte("A"), 100)
	p2 := bytes.Repeat([]byte("B"), 50)
	if _, err := ls.UploadPart("b", "big", uploadID, 1, bytes.NewReader(p1), int64(len(p1))); err != nil {
		t.Fatal(err)
	}
	if _, err := ls.UploadPart("b", "big", uploadID, 2, bytes.NewReader(p2), int64(len(p2))); err != nil {
		t.Fatal(err)
	}
	if err := ls.CompleteMultipartUpload("b", "big", uploadID, []CompletedPart{{PartNumber: 1}, {PartNumber: 2}}); err != nil {
		t.Fatal(err)
	}
	rc, _ := ls.GetObject("b", "big")
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if len(got) != 150 || !bytes.Equal(got[:100], p1) || !bytes.Equal(got[100:], p2) {
		t.Errorf("assembled object wrong: len=%d", len(got))
	}
	// The staging directory must be gone after completion.
	if _, err := os.Stat(filepath.Join(ls.rootPath, ".multipart", uploadID)); !os.IsNotExist(err) {
		t.Error("multipart staging dir not cleaned up")
	}
}

func TestCompleteWithNoPartsFails(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	uploadID, _ := ls.CreateMultipartUpload("b", "k", "", nil)
	if err := ls.CompleteMultipartUpload("b", "k", uploadID, nil); err == nil {
		t.Error("completing with zero parts should fail")
	}
}
