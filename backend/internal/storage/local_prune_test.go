package storage

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"bkt/internal/validation"
)

func mustExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); err != nil {
		t.Errorf("%s should exist: %v", p, err)
	}
}

func mustNotExist(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Errorf("%s should be gone (err=%v)", p, err)
	}
}

func TestPruneEmptyDirsBounds(t *testing.T) {
	root := t.TempDir()
	bucket := filepath.Join(root, "b")
	deep := filepath.Join(bucket, "x", "y", "z")
	if err := os.MkdirAll(deep, 0750); err != nil {
		t.Fatal(err)
	}
	pruneEmptyDirs(bucket, deep)
	mustNotExist(t, filepath.Join(bucket, "x"))
	mustExist(t, bucket) // the root itself is never removed

	// A dir outside root, or root itself, is never touched.
	other := filepath.Join(root, "other", "empty")
	_ = os.MkdirAll(other, 0750)
	pruneEmptyDirs(bucket, other)
	mustExist(t, other)
	pruneEmptyDirs(bucket, bucket)
	mustExist(t, bucket)

	// A regular file at a would-be directory path is never unlinked.
	f := filepath.Join(bucket, "file")
	if err := os.WriteFile(f, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	pruneEmptyDirs(bucket, f)
	mustExist(t, f)
}

func TestLocalDeletePrunesEmptyDirs(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	putString(t, ls, "a/b/c/one", "1")
	putString(t, ls, "a/keep", "k")
	if err := ls.DeleteObject("b", "a/b/c/one"); err != nil {
		t.Fatal(err)
	}
	mustNotExist(t, filepath.Join(ls.rootPath, "b", "a", "b"))
	mustExist(t, filepath.Join(ls.rootPath, "b", "a", "keep"))
	if err := ls.DeleteObject("b", "a/keep"); err != nil {
		t.Fatal(err)
	}
	mustNotExist(t, filepath.Join(ls.rootPath, "b", "a"))
	mustExist(t, filepath.Join(ls.rootPath, "b")) // bucket dir survives even when empty
	if got := listKeys(t, ls, ""); len(got) != 0 {
		t.Errorf("listing = %v", got)
	}
}

// A folder that only holds its marker is an explicit folder: deleting its
// last object keeps it; deleting the marker afterwards removes it.
func TestLocalDeleteKeepsExplicitFolder(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	putString(t, ls, "top/dir/", "")
	putString(t, ls, "top/dir/file", "f")
	if err := ls.DeleteObject("b", "top/dir/file"); err != nil {
		t.Fatal(err)
	}
	mustExist(t, filepath.Join(ls.rootPath, "b", "top", "dir", validation.LocalFolderMarkerName))
	if got := listKeys(t, ls, ""); len(got) != 1 || got[0] != "top/dir/" {
		t.Errorf("listing = %v", got)
	}
	if err := ls.DeleteObject("b", "top/dir/"); err != nil {
		t.Fatal(err)
	}
	mustNotExist(t, filepath.Join(ls.rootPath, "b", "top"))

	// Deleting a marker whose folder still has contents keeps the contents.
	putString(t, ls, "p/q/", "")
	putString(t, ls, "p/q/r/s", "s")
	if err := ls.DeleteObject("b", "p/q/"); err != nil {
		t.Fatal(err)
	}
	if got := readString(t, ls, "p/q/r/s"); got != "s" {
		t.Errorf("content lost: %q", got)
	}
}

func TestLocalVersionOpsPruneEmptyDirs(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	putString(t, ls, "v/w/obj", "one")
	if err := ls.ArchiveObjectVersion("b", "v/w/obj", testVID); err != nil {
		t.Fatal(err)
	}
	// Archiving moved the bytes out: the object's folders are gone.
	mustNotExist(t, filepath.Join(ls.rootPath, "b", "v"))
	if err := ls.PromoteObjectVersion("b", "v/w/obj", testVID); err != nil {
		t.Fatal(err)
	}
	if got := readString(t, ls, "v/w/obj"); got != "one" {
		t.Errorf("promoted = %q", got)
	}
	// Promoting moved the bytes out of version storage: its tree is pruned
	// down to (excluding) .versions/<bucket>.
	mustNotExist(t, filepath.Join(ls.rootPath, ".versions", "b", "v"))
	mustExist(t, filepath.Join(ls.rootPath, ".versions", "b"))

	if err := ls.ArchiveObjectVersion("b", "v/w/obj", testVID); err != nil {
		t.Fatal(err)
	}
	if err := ls.DeleteObjectVersion("b", "v/w/obj", testVID); err != nil {
		t.Fatal(err)
	}
	mustNotExist(t, filepath.Join(ls.rootPath, ".versions", "b", "v"))

	// Promoting a missing version fails cleanly (no retry loop confusion).
	if err := ls.PromoteObjectVersion("b", "v/w/obj", testVID); err == nil {
		t.Error("promoting a missing version succeeded")
	}
	mustNotExist(t, filepath.Join(ls.rootPath, "b", "v"))
}

func TestLocalFailedMultipartCompleteLeavesNoDirs(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	id, err := ls.CreateMultipartUpload("b", "m/n/obj", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Part 1 was never uploaded: assembly fails after the object dir exists.
	if err := ls.CompleteMultipartUpload("b", "m/n/obj", id, []CompletedPart{{PartNumber: 1}}); err == nil {
		t.Fatal("complete with a missing part succeeded")
	}
	mustNotExist(t, filepath.Join(ls.rootPath, "b", "m"))
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, fmt.Errorf("client went away") }

func TestLocalFailedPutLeavesNoDirs(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	if err := ls.PutObject("b", "f/g/obj", failingReader{}, 10, "", nil); err == nil {
		t.Fatal("put with a failing body succeeded")
	}
	mustNotExist(t, filepath.Join(ls.rootPath, "b", "f"))
}

// The writer side of the race: the directory is pruned between MkdirAll and
// the file creation. withDirRetry must re-create it and succeed.
func TestWithDirRetryRecreatesPrunedDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "b", "x", "y")
	calls := 0
	err := withDirRetry(dir, func() error {
		calls++
		if calls == 1 {
			// Simulate a concurrent delete pruning the fresh (empty) chain.
			pruneEmptyDirs(root, dir)
		}
		f, err := os.CreateTemp(dir, "t-*")
		if err != nil {
			return err
		}
		return f.Close()
	})
	if err != nil || calls != 2 {
		t.Fatalf("err=%v calls=%d, want nil/2", err, calls)
	}

	// A persistent non-ENOENT failure is returned without retrying.
	calls = 0
	if err := withDirRetry(dir, func() error { calls++; return syscall.EACCES }); err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

// Parallel puts, deletes, overwrites, listings and version churn in the same
// directories: no operation may fail because a sibling pruned a directory, and
// every object that was written and not deleted must survive intact.
func TestLocalConcurrentPutDeleteSameDir(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	dirs := []string{"hot/a/b/c", "hot/a/b", "hot/a", "hot"}

	deadline := time.Now().Add(1500 * time.Millisecond)
	if testing.Short() {
		deadline = time.Now().Add(300 * time.Millisecond)
	}
	var (
		wg     sync.WaitGroup
		errsMu sync.Mutex
		errs   []error
		ops    atomic.Int64
	)
	fail := func(err error) {
		errsMu.Lock()
		if len(errs) < 20 {
			errs = append(errs, err)
		}
		errsMu.Unlock()
	}
	kept := make([]map[string]string, 8)

	for g := 0; g < 8; g++ {
		kept[g] = map[string]string{}
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; time.Now().Before(deadline); i++ {
				dir := dirs[(g+i)%len(dirs)]
				tmpKey := fmt.Sprintf("%s/tmp-%d-%d", dir, g, i)
				body := fmt.Sprintf("body-%d-%d", g, i)
				// Churn: create then delete an object (pruning its dirs).
				if err := ls.PutObject("b", tmpKey, bytes.NewReader([]byte(body)), int64(len(body)), "", nil); err != nil {
					fail(fmt.Errorf("put %s: %w", tmpKey, err))
					continue
				}
				if rc, err := ls.GetObject("b", tmpKey); err != nil {
					fail(fmt.Errorf("get %s right after put: %w", tmpKey, err))
				} else {
					_ = rc.Close()
				}
				if err := ls.DeleteObject("b", tmpKey); err != nil {
					fail(fmt.Errorf("delete %s: %w", tmpKey, err))
				}
				// Every few rounds keep an object for good.
				if i%7 == 0 {
					keepKey := fmt.Sprintf("%s/keep-%d-%d", dir, g, i)
					if err := ls.PutObject("b", keepKey, bytes.NewReader([]byte(body)), int64(len(body)), "", nil); err != nil {
						fail(fmt.Errorf("put %s: %w", keepKey, err))
					} else {
						kept[g][keepKey] = body
					}
				}
				// Version churn in the same trees (.versions side included).
				if i%5 == 0 {
					vkey := fmt.Sprintf("%s/ver-%d", dir, g)
					vid := fmt.Sprintf("0b0c3b51-0000-4000-8000-%012d", g*1_000_000+i)
					if err := ls.PutObject("b", vkey, bytes.NewReader([]byte(body)), int64(len(body)), "", nil); err != nil {
						fail(fmt.Errorf("put %s: %w", vkey, err))
					} else if err := ls.ArchiveObjectVersion("b", vkey, vid); err != nil {
						fail(fmt.Errorf("archive %s: %w", vkey, err))
					} else if err := ls.PromoteObjectVersion("b", vkey, vid); err != nil {
						fail(fmt.Errorf("promote %s: %w", vkey, err))
					} else if err := ls.ArchiveObjectVersion("b", vkey, vid); err != nil {
						fail(fmt.Errorf("re-archive %s: %w", vkey, err))
					} else if err := ls.DeleteObjectVersion("b", vkey, vid); err != nil {
						fail(fmt.Errorf("delete version %s: %w", vkey, err))
					}
				}
				ops.Add(1)
			}
		}(g)
	}
	// Concurrent listings must never fail on a vanished file/directory.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for time.Now().Before(deadline) {
			if _, err := ls.ListObjects("b", "hot/"); err != nil {
				fail(fmt.Errorf("list: %w", err))
			}
		}
	}()
	wg.Wait()

	for _, err := range errs {
		t.Error(err)
	}
	if ops.Load() == 0 {
		t.Fatal("no operations ran")
	}
	want := 0
	for _, m := range kept {
		for k, body := range m {
			want++
			rc, err := ls.GetObject("b", k)
			if err != nil {
				t.Errorf("kept object %s lost: %v", k, err)
				continue
			}
			got, _ := io.ReadAll(rc)
			_ = rc.Close()
			if string(got) != body {
				t.Errorf("kept object %s = %q, want %q", k, got, body)
			}
		}
	}
	if got := listKeys(t, ls, "hot/"); len(got) != want {
		t.Errorf("listing has %d objects, want %d kept", len(got), want)
	}
	// All version storage was deleted again: nothing but the bucket root left.
	if entries, err := os.ReadDir(filepath.Join(ls.rootPath, ".versions", "b")); err == nil && len(entries) != 0 {
		t.Errorf("version storage not pruned: %d entries left", len(entries))
	}
	t.Logf("%d rounds", ops.Load())
}
