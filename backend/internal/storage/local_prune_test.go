package storage

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
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

func versionDirOf(ls *LocalStorage, key string) string {
	return filepath.Join(ls.rootPath, ".objversions", "b", keyHash(key))
}

func TestLocalDeleteRemovesBlobAndSidecar(t *testing.T) {
	ls := newTestLocal(t)
	putString(t, ls, "a/b/c/one", "1")
	putString(t, ls, "a/keep", "k")
	loc, _ := ls.objectLocation("b", "a/b/c/one")
	mustExist(t, loc.blob)
	mustExist(t, loc.sidecar)
	if err := ls.DeleteObject("b", "a/b/c/one"); err != nil {
		t.Fatal(err)
	}
	mustNotExist(t, loc.blob)
	mustNotExist(t, loc.sidecar)
	// Deleting a missing key is a no-op success.
	if err := ls.DeleteObject("b", "a/b/c/one"); err != nil {
		t.Fatal(err)
	}
	if got := listKeys(t, ls, ""); len(got) != 1 || got[0] != "a/keep" {
		t.Errorf("listing = %v", got)
	}
}

func TestLocalVersionDirLifecycle(t *testing.T) {
	ls := newTestLocal(t)
	putString(t, ls, "v/w/obj", "one")
	loc, _ := ls.objectLocation("b", "v/w/obj")
	if err := ls.ArchiveObjectVersion("b", "v/w/obj", testVID); err != nil {
		t.Fatal(err)
	}
	vdir := versionDirOf(ls, "v/w/obj")
	mustExist(t, filepath.Join(vdir, testVID))
	if got, err := os.ReadFile(filepath.Join(vdir, ".key")); err != nil || string(got) != "v/w/obj" {
		t.Errorf("version sidecar = %q, %v", got, err)
	}
	// Archiving moved the bytes out: the object's blob and sidecar are gone.
	mustNotExist(t, loc.blob)
	mustNotExist(t, loc.sidecar)

	if err := ls.PromoteObjectVersion("b", "v/w/obj", testVID); err != nil {
		t.Fatal(err)
	}
	if got := readString(t, ls, "v/w/obj"); got != "one" {
		t.Errorf("promoted = %q", got)
	}
	mustExist(t, loc.sidecar)
	// The last version left: the per-key version dir is removed, the
	// bucket's version root stays.
	mustNotExist(t, vdir)
	mustExist(t, filepath.Join(ls.rootPath, ".objversions", "b"))

	const vid2 = "0b0c3b51-0000-4000-8000-0000000000ab"
	if err := ls.ArchiveObjectVersion("b", "v/w/obj", testVID); err != nil {
		t.Fatal(err)
	}
	putString(t, ls, "v/w/obj", "two")
	if err := ls.ArchiveObjectVersion("b", "v/w/obj", vid2); err != nil {
		t.Fatal(err)
	}
	if err := ls.DeleteObjectVersion("b", "v/w/obj", testVID); err != nil {
		t.Fatal(err)
	}
	mustExist(t, filepath.Join(vdir, ".key")) // vid2 still there
	if err := ls.DeleteObjectVersion("b", "v/w/obj", vid2); err != nil {
		t.Fatal(err)
	}
	mustNotExist(t, vdir)

	// Promoting a missing version fails cleanly and leaves nothing behind.
	if err := ls.PromoteObjectVersion("b", "v/w/obj", testVID); err == nil {
		t.Error("promoting a missing version succeeded")
	}
	mustNotExist(t, loc.sidecar)
	mustNotExist(t, vdir)
	// A failed archive (no current object) leaves no version dir behind.
	if err := ls.ArchiveObjectVersion("b", "v/w/obj", testVID); err == nil {
		t.Error("archiving a missing object succeeded")
	}
	mustNotExist(t, vdir)
}

func TestLocalFailedMultipartCompleteLeavesNothing(t *testing.T) {
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
	assertNoFiles(t, filepath.Join(ls.rootPath, ".objects", "b"))
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, fmt.Errorf("client went away") }

func TestLocalFailedPutLeavesNothing(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	if err := ls.PutObject("b", "f/g/obj", failingReader{}, 10, "", nil); err == nil {
		t.Fatal("put with a failing body succeeded")
	}
	assertNoFiles(t, filepath.Join(ls.rootPath, ".objects", "b"))

	// A failed overwrite keeps the previous object and its sidecar.
	putString(t, ls, "f/g/obj", "old")
	if err := ls.PutObject("b", "f/g/obj", failingReader{}, 10, "", nil); err == nil {
		t.Fatal("put with a failing body succeeded")
	}
	if got := readString(t, ls, "f/g/obj"); got != "old" {
		t.Errorf("object after failed overwrite = %q", got)
	}
	if got := listKeys(t, ls, ""); len(got) != 1 {
		t.Errorf("listing = %v", got)
	}
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
			// Simulate a concurrent prune removing the fresh (empty) dir.
			_ = syscall.Rmdir(dir)
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
// "folders": no operation may fail, and every object that was written and not
// deleted must survive intact.
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
				// Churn: create then delete an object.
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
				// Version churn (per-key version dirs created and pruned).
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
	if entries, err := os.ReadDir(filepath.Join(ls.rootPath, ".objversions", "b")); err == nil && len(entries) != 0 {
		t.Errorf("version storage not pruned: %d entries left", len(entries))
	}
	assertSidecarInvariant(t, ls)
	t.Logf("%d rounds", ops.Load())
}

func TestRetryableDirErr(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "a", "b")
	if !retryableDirErr(dir, &fs.PathError{Op: "mkdir", Path: dir, Err: syscall.EEXIST}) {
		t.Error("stale EEXIST with nothing on the path should be retryable")
	}
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if retryableDirErr(dir, &fs.PathError{Op: "mkdir", Path: dir, Err: syscall.EEXIST}) {
		t.Error("EEXIST caused by a regular file on the path must not be retried")
	}
	if !retryableDirErr(dir, &fs.PathError{Op: "mkdir", Path: dir, Err: syscall.ENOENT}) {
		t.Error("ENOENT should be retryable")
	}
	if retryableDirErr(dir, &fs.PathError{Op: "mkdir", Path: dir, Err: syscall.EACCES}) {
		t.Error("EACCES must not be retried")
	}
}

func TestWithDirRetryDoesNotRetryOpEEXIST(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	err := withDirRetry(dir, func() error {
		calls++
		return &fs.PathError{Op: "link", Path: dir, Err: syscall.EEXIST}
	})
	if !errors.Is(err, fs.ErrExist) || calls != 1 {
		t.Fatalf("op EEXIST: err=%v calls=%d, want one call returning EEXIST", err, calls)
	}
}

// assertSidecarInvariant checks "blob exists ⇒ sidecar exists" for objects
// and "version dir holds versions ⇒ sidecar exists" for versions.
func assertSidecarInvariant(t *testing.T, ls *LocalStorage) {
	t.Helper()
	_ = filepath.WalkDir(filepath.Join(ls.rootPath, ".objects", "b"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && isBlobName(d.Name()) {
			if _, serr := os.Lstat(p + ".key"); serr != nil {
				t.Errorf("blob %s has no sidecar", p)
			}
		}
		return nil
	})
	vroot := filepath.Join(ls.rootPath, ".objversions", "b")
	entries, _ := os.ReadDir(vroot)
	for _, e := range entries {
		sub, _ := os.ReadDir(filepath.Join(vroot, e.Name()))
		versions, sidecar := 0, false
		for _, f := range sub {
			if f.Name() == ".key" {
				sidecar = true
			} else if !strings.HasPrefix(f.Name(), ".") {
				versions++
			}
		}
		if versions > 0 && !sidecar {
			t.Errorf("version dir %s has %d versions but no sidecar", e.Name(), versions)
		}
	}
}

// One writer and one remover racing on the SAME key (which the API layer
// serializes, but a second process or a bug might not) never leave a blob or
// a version without its key sidecar.
func TestLocalSidecarInvariantUnderSameKeyRaces(t *testing.T) {
	ls := newTestLocal(t)
	_ = ls.CreateBucket("b", "")
	deadline := time.Now().Add(800 * time.Millisecond)
	if testing.Short() {
		deadline = time.Now().Add(200 * time.Millisecond)
	}
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; time.Now().Before(deadline); i++ {
				vid := fmt.Sprintf("0b0c3b51-0000-4000-8000-%012d", g*1_000_000+i)
				switch g {
				case 0:
					_ = ls.PutObject("b", "k", bytes.NewReader([]byte("x")), 1, "", nil)
				case 1:
					_ = ls.DeleteObject("b", "k")
				case 2:
					if ls.ArchiveObjectVersion("b", "k", vid) == nil {
						_ = ls.DeleteObjectVersion("b", "k", vid)
					}
				case 3:
					_ = ls.PutObject("b", "k", bytes.NewReader([]byte("y")), 1, "", nil)
					if ls.ArchiveObjectVersion("b", "k", vid) == nil && i%2 == 0 {
						_ = ls.PromoteObjectVersion("b", "k", vid)
					}
				}
			}
		}(g)
	}
	wg.Wait()
	assertSidecarInvariant(t, ls)
}
