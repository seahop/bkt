package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbeWritable_OK(t *testing.T) {
	root := t.TempDir()
	// An existing bucket with an object and the internal dirs.
	for _, d := range []string{"bucket-a/nested", ".multipart/u1", ".versions/bucket-a"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "bucket-a", "nested", "obj"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ProbeWritable(root); err != nil {
		t.Fatalf("ProbeWritable on a healthy root: %v", err)
	}
	// The probe must leave no temp files behind.
	_ = filepath.Walk(root, func(p string, _ os.FileInfo, _ error) error {
		if strings.HasPrefix(filepath.Base(p), probeTempPrefix) {
			t.Errorf("probe left a temp file behind: %s", p)
		}
		return nil
	})
}

func TestProbeWritable_EmptyRoot(t *testing.T) {
	if err := ProbeWritable(t.TempDir()); err != nil {
		t.Fatalf("ProbeWritable on an empty root: %v", err)
	}
}

func TestProbeWritable_NotADirectory(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ProbeWritable(f); err == nil {
		t.Fatal("expected an error for a non-directory root")
	}
}

func TestProbeWritable_Missing(t *testing.T) {
	if err := ProbeWritable(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected an error for a missing root")
	}
}

// Permission failures cannot be simulated as root (root bypasses mode bits).
func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("running as root: permission bits are not enforced")
	}
}

func TestProbeWritable_ReadOnlyRoot(t *testing.T) {
	skipIfRoot(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	err := ProbeWritable(root)
	if err == nil {
		t.Fatal("expected an error for a read-only root")
	}
	if !strings.Contains(err.Error(), "chown -R 10001:10001 "+root) {
		t.Errorf("error should tell the operator how to fix ownership, got: %v", err)
	}
}

func TestProbeWritable_UnwritableBucket(t *testing.T) {
	skipIfRoot(t)
	root := t.TempDir()
	bucket := filepath.Join(root, "old-bucket")
	if err := os.Mkdir(bucket, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(bucket, 0o700) })
	if err := ProbeWritable(root); err == nil || !strings.Contains(err.Error(), "old-bucket") {
		t.Fatalf("expected an error naming the unwritable bucket, got: %v", err)
	}
}

func TestProbeWritable_UnreadableObject(t *testing.T) {
	skipIfRoot(t)
	root := t.TempDir()
	bucket := filepath.Join(root, "b")
	if err := os.Mkdir(bucket, 0o750); err != nil {
		t.Fatal(err)
	}
	obj := filepath.Join(bucket, "secret")
	if err := os.WriteFile(obj, []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	err := ProbeWritable(root)
	if err == nil || !strings.Contains(err.Error(), obj) {
		t.Fatalf("expected an error naming the unreadable object, got: %v", err)
	}
}
