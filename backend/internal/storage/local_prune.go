package storage

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// dirRaceAttempts bounds how often a writer re-creates its parent directory
// when a concurrent delete prunes it between MkdirAll and the file operation
// (see pruneEmptyDirs). Each retry only happens after an ENOENT caused by a
// directory that was empty at that instant, so a handful is plenty.
const dirRaceAttempts = 8

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
// into dir. If op (or MkdirAll itself, whose intermediate directories can be
// pruned while it runs) fails with ENOENT — the directory was pruned by a
// concurrent delete in between — the directory is re-created and op retried.
// op must be safe to repeat after an ENOENT failure.
func withDirRetry(dir string, op func() error) error {
	var err error
	for attempt := 0; attempt < dirRaceAttempts; attempt++ {
		if err = os.MkdirAll(dir, 0750); err != nil {
			if isNotExist(err) {
				continue
			}
			return err
		}
		if err = op(); !isNotExist(err) {
			return err
		}
	}
	return err
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
