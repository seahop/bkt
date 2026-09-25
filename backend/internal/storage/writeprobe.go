package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

// Bounds for the startup probe: it must stay cheap on stores with many
// buckets and millions of objects.
const (
	probeMaxDirs         = 16  // directories checked per level (top level, .objects/*, .objversions/*)
	probeMaxFilesPerDir  = 4   // existing files opened for reading per checked dir
	probeMaxWalkPerDir   = 200 // directory entries visited per checked dir while sampling
	probeTempPrefix      = ".tmp-upload-writeprobe-"
	probeImageUID        = 10001
	probeImageGIDDefault = 10001
)

// ProbeWritable verifies that the process can actually use the local storage
// root: it creates and removes a temp file in root, and for a bounded sample of
// the existing directories checks that they are listable and writable and that
// a few existing files inside them are readable. The sample covers the
// top-level directories (the blob-layout trees .objects/.objversions, the
// .multipart staging area and — until the startup migration has converted
// them — legacy bucket and .versions directories) and the per-bucket
// directories inside .objects and .objversions.
//
// os.MkdirAll succeeds on an existing directory regardless of its owner, so
// without this probe a volume written by an older root-run bkt image looks
// healthy at startup under the unprivileged uid 10001 while every write fails
// and every 0600 root-owned object is unreadable. The returned error says
// exactly how to fix the ownership.
//
// Temp files use the ".tmp-upload-" prefix, which listings and the layout
// migration already skip, so a probe interrupted mid-way never surfaces as an
// object.
func ProbeWritable(root string) error {
	info, err := os.Stat(root)
	if err != nil {
		return probeError(root, root, "cannot stat storage root", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("storage root %s is not a directory (check STORAGE_ROOT)", root)
	}

	if err := probeDirWritable(root); err != nil {
		return probeError(root, root, "storage root is not writable", err)
	}

	if err := probeChildren(root, root); err != nil {
		return err
	}
	// Bucket directories of the blob layout (a missing tree is fine: it is
	// created on first use / by the migration).
	for _, tree := range []string{objectsDirName, objVersionsDirName} {
		dir := filepath.Join(root, tree)
		if _, err := os.Stat(dir); err != nil {
			if isNotExist(err) {
				continue
			}
			return probeError(root, dir, "existing storage directory is not accessible", err)
		}
		if err := probeChildren(root, dir); err != nil {
			return err
		}
	}
	return nil
}

// probeChildren probes up to probeMaxDirs sub-directories of parent (sorted
// by name): each must be listable and writable, and a few files in it
// readable.
func probeChildren(root, parent string) error {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return probeError(root, parent, "storage directory is not readable", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	checked := 0
	for _, e := range entries {
		if checked >= probeMaxDirs {
			break
		}
		if !e.IsDir() {
			continue
		}
		checked++
		dir := filepath.Join(parent, e.Name())
		if _, err := os.ReadDir(dir); err != nil {
			return probeError(root, dir, "existing storage directory is not readable", err)
		}
		if err := probeDirWritable(dir); err != nil {
			return probeError(root, dir, "existing storage directory is not writable", err)
		}
		if path, err := probeSampleFiles(dir); err != nil {
			return probeError(root, path, "existing stored data is not readable", err)
		}
	}
	return nil
}

// probeDirWritable creates, writes and removes a temp file in dir.
func probeDirWritable(dir string) error {
	f, err := os.CreateTemp(dir, probeTempPrefix+"*")
	if err != nil {
		return err
	}
	name := f.Name()
	_, werr := f.Write([]byte("bkt"))
	cerr := f.Close()
	rerr := os.Remove(name)
	return errors.Join(werr, cerr, rerr)
}

// probeSampleFiles opens up to probeMaxFilesPerDir regular files (and descends
// into sub-directories) under dir, visiting at most probeMaxWalkPerDir entries.
// It returns the offending path and error on the first failure.
func probeSampleFiles(dir string) (string, error) {
	files, visited := 0, 0
	var badPath string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			badPath = path
			return walkErr
		}
		visited++
		if visited > probeMaxWalkPerDir || files >= probeMaxFilesPerDir {
			return fs.SkipAll
		}
		if !d.Type().IsRegular() {
			return nil
		}
		f, err := os.Open(path) //nolint:gosec // path comes from walking the configured storage root
		if err != nil {
			badPath = path
			return err
		}
		_ = f.Close()
		files++
		return nil
	})
	if err != nil {
		return badPath, err
	}
	return "", nil
}

// probeError builds the startup error, including the remediation for the
// common case (data written by the old root-run image, now owned by root).
func probeError(root, path, what string, err error) error {
	uid, gid := os.Getuid(), os.Getgid()
	owner := ""
	if fi, statErr := os.Stat(path); statErr == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			owner = fmt.Sprintf(" (owned by %d:%d, mode %s)", st.Uid, st.Gid, fi.Mode().Perm())
		}
	}
	return fmt.Errorf(
		"%s: %s%s for the bkt process running as uid %d gid %d: %w\n"+
			"This usually means the volume was written by a bkt image up to v1.4.0, which ran as root; "+
			"newer images run as the unprivileged uid/gid %d. Fix it once, then restart:\n"+
			"  - chown the data:   chown -R %d:%d %s\n"+
			"      e.g. docker run --rm -v <volume-or-host-dir>:/data alpine chown -R %d:%d /data\n"+
			"  - Kubernetes: set podSecurityContext.fsGroup=%d (volumes that ignore fsGroup, like hostPath/NFS, need the chown above or an init container running as root that does it)\n"+
			"  - or run the container as root (docker: --user 0:0 / compose: user: \"0:0\") — not recommended",
		what, path, owner, uid, gid, err,
		probeImageUID,
		probeImageUID, probeImageGIDDefault, root,
		probeImageUID, probeImageGIDDefault,
		probeImageGIDDefault,
	)
}
