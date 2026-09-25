package storage

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// dirRaceAttempts bounds how often a writer re-creates its parent directory
// when a concurrent delete prunes it between MkdirAll and the file operation
// (see pruneEmptyDirs). A retry only happens when a directory on the path was
// empty and pruned at that instant; under heavy churn in one tree that can
// repeat a few times, so allow plenty of attempts with a short backoff.
const dirRaceAttempts = 64

// pruneEmptyDirs removes dir and then each of its parents while they are
// empty, stopping at the first directory that cannot be removed (non-empty,
// already gone, permission, ...) and never removing root itself or anything
// outside it. It is called after a file was removed or moved away so deletes
// don't leave empty directory trees behind.
//
// It is race-safe by construction: rmdir(2) only succeeds on an empty
// directory, so a directory that a concurrent writer has just put a file (or
// a temp file) into is never removed. A writer that loses the race — its
// directory is pruned between MkdirAll and creating its file — sees ENOENT and
// retries (see withDirRetry). syscall.Rmdir is used instead of os.Remove so
// that a path which was concurrently replaced by a regular file (an object
// "a/b" written after the folder "a/b/" was pruned) is never unlinked.
//
// A folder that holds only its folder-marker file (".bkt-folder") is not
// empty — explicit folders survive the deletion of their contents.
func pruneEmptyDirs(root, dir string) {
	root = filepath.Clean(root)
	dir = filepath.Clean(dir)
	for {
		rel, err := filepath.Rel(root, dir)
		if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) ||
			strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return
		}
		if err := syscall.Rmdir(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// isNotExist reports ENOENT, also through wrapped errors (os.IsNotExist does
// not unwrap).
func isNotExist(err error) bool {
	return err != nil && errors.Is(err, fs.ErrNotExist)
}

// withDirRetry ensures dir exists and runs op, which creates or moves a file
// into dir. If MkdirAll fails because a concurrent delete pruned a directory on
// the path while it ran (see retryableDirErr), or op fails with ENOENT because
// the directory was pruned between MkdirAll and op, the directory is
// re-created and op retried. op must be safe to repeat after an ENOENT.
func withDirRetry(dir string, op func() error) error {
	var err error
	for attempt := 0; attempt < dirRaceAttempts; attempt++ {
		if attempt > 0 {
			dirRaceBackoff(attempt)
		}
		if err = os.MkdirAll(dir, 0750); err != nil {
			if retryableDirErr(dir, err) {
				continue
			}
			return err
		}
		// Only ENOENT is a race for op: an EEXIST from op is real (e.g.
		// renameNoReplace refusing to overwrite an archived version).
		if err = op(); !isNotExist(err) {
			return err
		}
	}
	return err
}

// retryableDirErr reports whether a MkdirAll error is a transient effect of a
// concurrent prune rather than a real conflict:
//   - ENOENT: the directory (or a parent) was removed between MkdirAll and op,
//     or while MkdirAll was walking the path;
//   - EEXIST from MkdirAll: its mkdir(2) lost to another creator and its
//     follow-up Lstat then found the directory already pruned again, so it
//     returned the stale EEXIST. That is transient only if nothing but
//     directories exists on the path — a regular file in the way (an object
//     "a/b" versus a folder "a/b/") is a genuine conflict and is returned.
func retryableDirErr(dir string, err error) bool {
	if isNotExist(err) {
		return true
	}
	if !errors.Is(err, fs.ErrExist) {
		return false
	}
	for p := filepath.Clean(dir); ; {
		fi, lerr := os.Lstat(p)
		if lerr == nil {
			return fi.IsDir()
		}
		if !isNotExist(lerr) {
			return false
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		p = parent
	}
}

// dirRaceBackoff yields, then sleeps a little longer on each attempt (at most
// ~6ms) so a writer doesn't spin against a pruner working on the same tree.
func dirRaceBackoff(attempt int) {
	runtime.Gosched()
	if attempt > 2 {
		time.Sleep(time.Duration(attempt) * 100 * time.Microsecond)
	}
}

// bucketDir / versionsBucketDir are the prune roots for a bucket's objects
// and for its archived versions (<root>/.versions/<bucket>).
func (ls *LocalStorage) bucketDir(bucketName string) (string, error) {
	return ls.resolve(bucketName)
}

func (ls *LocalStorage) versionsBucketDir(bucketName string) (string, error) {
	return ls.resolve(".versions", bucketName)
}

// pruneObjectParents prunes the now-possibly-empty parent directories of an
// object file that was removed or moved away, up to (excluding) the bucket
// directory.
func (ls *LocalStorage) pruneObjectParents(bucketName, removedPath string) {
	if root, err := ls.bucketDir(bucketName); err == nil {
		pruneEmptyDirs(root, filepath.Dir(removedPath))
	}
}

// pruneVersionParents is pruneObjectParents for version storage: it prunes up
// to (excluding) <root>/.versions/<bucket>.
func (ls *LocalStorage) pruneVersionParents(bucketName, removedPath string) {
	if root, err := ls.versionsBucketDir(bucketName); err == nil {
		pruneEmptyDirs(root, filepath.Dir(removedPath))
	}
}
