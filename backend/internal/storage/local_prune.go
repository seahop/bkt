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

// Directory lifecycle in the blob layout:
//
//   - Object fan-out directories (.objects/<bucket>/xx/yy) are created on
//     demand and never pruned. There are at most 65,536 of them per bucket, so
//     keeping them is cheap, and it removes the whole "writer's directory was
//     pruned under it" race class from every object write.
//   - Per-key version directories (.objversions/<bucket>/<sha256(key)>) are
//     unbounded (one per versioned key), so they are removed once their last
//     version is deleted or promoted (pruneVersionDir). Writers into them
//     (ArchiveObjectVersion) use withDirRetry to survive a concurrent prune.

// dirRaceAttempts bounds how often a writer re-creates its directory when a
// concurrent prune removes it between MkdirAll and the file operation.
const dirRaceAttempts = 64

// pruneVersionDir removes a key's version directory once it holds no version
// any more (only its ".key" sidecar). rmdir(2) only succeeds on an empty
// directory, so a version (or a sidecar temp file) that a concurrent archive
// has just put into it keeps it alive; in that case the sidecar removed here
// is restored.
func pruneVersionDir(vdir, key string) {
	entries, err := os.ReadDir(vdir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() != versionSidecarName {
			return // versions (or a writer's temp file) still present
		}
	}
	sidecar := filepath.Join(vdir, versionSidecarName)
	if err := os.Remove(sidecar); err != nil && !isNotExist(err) {
		return
	}
	if err := syscall.Rmdir(vdir); err == nil || isNotExist(err) {
		return
	}
	// A concurrent archive won the race: keep its version described.
	if entries, err := os.ReadDir(vdir); err == nil {
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), ".") {
				_ = ensureSidecar(vdir, sidecar, key)
				return
			}
		}
	}
}

// isNotExist reports ENOENT, also through wrapped errors (os.IsNotExist does
// not unwrap).
func isNotExist(err error) bool {
	return err != nil && errors.Is(err, fs.ErrNotExist)
}

// withDirRetry ensures dir exists and runs op, which creates or moves a file
// into dir. If MkdirAll fails because a concurrent prune removed a directory
// on the path while it ran (see retryableDirErr), or op fails with ENOENT
// because the directory was pruned between MkdirAll and op, the directory is
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
//     directories exists on the path — a regular file in the way is a genuine
//     conflict and is returned.
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
