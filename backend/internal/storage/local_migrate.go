package storage

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"bkt/internal/logger"
	"bkt/internal/validation"
)

// Legacy (key-as-path) layout, as written by releases before the blob layout:
//
//	<root>/<bucket>/<key>                          object bytes ("a/b/c.txt")
//	<root>/<bucket>/<dir>/.bkt-folder              folder-marker object "<dir>/"
//	<root>/<bucket>/<dir>                          (older releases) a zero-length
//	                                               marker "<dir>/" — indistinguishable
//	                                               from an object "<dir>"
//	<root>/<bucket>/**/.tmp-upload-* .tmp-assemble-*  interrupted writes
//	<root>/.versions/<bucket>/<key>/<versionID>    archived versions
//	<root>/.versions/<bucket>/<dir>/.bkt-folder/<versionID>  versions of "<dir>/"
//	<root>/.versions/<bucket>/<dir>/<versionID>    (older releases) versions of a
//	                                               marker "<dir>/" — indistinguishable
//	                                               from versions of "<dir>"
//
// MigrateLocalLayout moves all of it into the blob layout (see local.go).

// legacyFolderMarkerName is the file the legacy layout stored a folder-marker
// object "<dir>/" in ("<dir>/.bkt-folder").
const legacyFolderMarkerName = ".bkt-folder"

// migrationProgressEvery is how often (in files) a long bucket migration logs
// progress.
const migrationProgressEvery = 10000

// layoutMigrationStats counts what a migration run did.
type layoutMigrationStats struct {
	Buckets     int // buckets migrated in this run
	Objects     int // object files moved into the blob layout
	Versions    int // version files moved into the blob layout
	Duplicates  int // legacy files removed because the blob layout already held identical bytes
	Conflicts   int // legacy files kept because the blob layout held DIFFERENT bytes
	SkippedTemp int // interrupted-write temp files left in place
	Unmappable  int // files that are not objects/versions (symlinks, bad version names, ...)
	// MarkerAliases counts empty "<dir>/" blobs added next to zero-length
	// legacy files "<dir>": releases before folder-marker support stored the
	// marker "<dir>/" as a plain empty file "<dir>", indistinguishable from an
	// empty object "<dir>", so both keys are provided (only the one the
	// database references is ever served; the other is an unlisted empty file).
	MarkerAliases int
}

func (s *layoutMigrationStats) add(o layoutMigrationStats) {
	s.Buckets += o.Buckets
	s.Objects += o.Objects
	s.Versions += o.Versions
	s.Duplicates += o.Duplicates
	s.Conflicts += o.Conflicts
	s.SkippedTemp += o.SkippedTemp
	s.Unmappable += o.Unmappable
	s.MarkerAliases += o.MarkerAliases
}

func (s layoutMigrationStats) leftovers() int { return s.Conflicts + s.SkippedTemp + s.Unmappable }

// MigrateLocalLayout converts the local storage root from the legacy layout
// (object keys used as file paths) to the blob layout. It must run before the
// server starts serving requests, with no other bkt process using root.
//
// Per bucket it moves every legacy object and version file (after writing its
// key sidecar) with an atomic same-filesystem rename, removes the emptied
// legacy directories and finally writes <root>/.objects/<bucket>/.layout-v2,
// so later starts skip the bucket. It is idempotent and crash-safe: a file is
// either still in the legacy tree or already in the blob layout (a crash
// between the hard link and the unlink of a move leaves both names of one
// inode, which the next run recognizes as a duplicate). A key present in both
// layouts keeps the blob-layout copy; the legacy copy is removed only when it
// is byte-identical and otherwise kept and reported. Nothing is ever deleted
// that has not been verified to exist in the blob layout.
//
// An I/O error aborts the run with an error (and no completion marker for the
// affected bucket); fixing the cause and restarting resumes where it stopped.
func MigrateLocalLayout(root string) error {
	_, err := migrateLocalLayout(root)
	return err
}

func migrateLocalLayout(root string) (layoutMigrationStats, error) {
	var total layoutMigrationStats
	buckets, err := legacyBuckets(root)
	if err != nil {
		return total, err
	}
	var pending []string
	for _, b := range buckets {
		if _, err := os.Lstat(filepath.Join(root, objectsDirName, b, layoutMarkerName)); err == nil {
			logger.Warn("Local storage: legacy directories of an already migrated bucket are still present; they hold files the migration could not place (see the migration log) and are not touched", map[string]interface{}{
				"bucket":         b,
				"legacy_objects": filepath.Join(root, b),
				"legacy_version": filepath.Join(root, legacyVersionsDir, b),
			})
			continue
		} else if !isNotExist(err) {
			return total, fmt.Errorf("checking migration marker of bucket %q: %w", b, err)
		}
		pending = append(pending, b)
	}
	if len(pending) == 0 {
		return total, nil
	}

	start := time.Now()
	logger.Info("Local storage: migrating buckets from the legacy key-as-path layout to the blob layout (one-time, resumable)", map[string]interface{}{
		"root": root, "buckets": len(pending),
	})
	for _, dir := range []string{objectsDirName, objVersionsDirName} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0750); err != nil {
			return total, fmt.Errorf("creating %s: %w", dir, err)
		}
	}
	for _, b := range pending {
		st, err := migrateLegacyBucket(root, b)
		total.add(st)
		if err != nil {
			logger.Error("Local storage migration stopped; fix the cause and restart bkt to resume", map[string]interface{}{
				"bucket": b, "error": err.Error(),
			})
			return total, fmt.Errorf("migrating bucket %q: %w", b, err)
		}
	}
	// The legacy version root is shared by all buckets; drop it once empty.
	_ = syscall.Rmdir(filepath.Join(root, legacyVersionsDir))

	logger.Info("Local storage: migration to the blob layout finished", map[string]interface{}{
		"buckets": total.Buckets, "objects_moved": total.Objects, "versions_moved": total.Versions, "marker_aliases": total.MarkerAliases,
		"duplicates_removed": total.Duplicates, "conflicts_kept": total.Conflicts,
		"temp_files_skipped": total.SkippedTemp, "unmappable_kept": total.Unmappable,
		"duration": time.Since(start).Round(time.Millisecond).String(),
	})
	return total, nil
}

// legacyBuckets returns the sorted names of buckets with a legacy object
// directory (<root>/<bucket>) and/or legacy version directory
// (<root>/.versions/<bucket>). Only names that are valid bucket names count:
// anything else (lost+found, operator files) is not bkt's and is left alone.
func legacyBuckets(root string) ([]string, error) {
	set := map[string]bool{}
	scan := func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if isNotExist(err) {
				return nil
			}
			return fmt.Errorf("reading %s: %w", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") || name == "lost+found" {
				continue // .objects, .objversions, .multipart, .versions, probe temp files; fsck's dir on a volume root
			}
			if validation.ValidateBucketName(name) != nil {
				if e.IsDir() {
					logger.Warn("Local storage: ignoring directory that is not a bucket", map[string]interface{}{"path": strconv.Quote(filepath.Join(dir, name))})
				}
				continue
			}
			if e.Type()&fs.ModeSymlink != 0 {
				logger.Error("Local storage: bucket directory is a symlink; it is NOT migrated (move its contents to a real directory on the same filesystem and restart)", map[string]interface{}{"path": strconv.Quote(filepath.Join(dir, name))})
				continue
			}
			if e.IsDir() {
				set[name] = true
			}
		}
		return nil
	}
	if err := scan(root); err != nil {
		return nil, err
	}
	if err := scan(filepath.Join(root, legacyVersionsDir)); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(set))
	for b := range set {
		out = append(out, b)
	}
	sort.Strings(out)
	return out, nil
}

type bucketMigrator struct {
	root, bucket string
	objRoot      string // <root>/.objects/<bucket>
	verRoot      string // <root>/.objversions/<bucket>
	stats        layoutMigrationStats
	seen         int
}

func migrateLegacyBucket(root, bucket string) (layoutMigrationStats, error) {
	m := &bucketMigrator{
		root:    root,
		bucket:  bucket,
		objRoot: filepath.Join(root, objectsDirName, bucket),
		verRoot: filepath.Join(root, objVersionsDirName, bucket),
	}
	if err := os.MkdirAll(m.objRoot, 0750); err != nil {
		return m.stats, err
	}
	legacyObjects := filepath.Join(root, bucket)
	legacyVersions := filepath.Join(root, legacyVersionsDir, bucket)
	if err := m.walk(legacyObjects, m.migrateObject); err != nil {
		return m.stats, err
	}
	if err := m.walk(legacyVersions, m.migrateVersion); err != nil {
		return m.stats, err
	}
	removeEmptyDirs(legacyObjects)
	removeEmptyDirs(legacyVersions)

	marker := filepath.Join(m.objRoot, layoutMarkerName)
	content := fmt.Sprintf("bkt local storage layout v2 (blob layout); migrated %s\n", time.Now().UTC().Format(time.RFC3339))
	if err := writeSmallFileAtomic(m.objRoot, marker, content); err != nil {
		return m.stats, fmt.Errorf("writing migration marker: %w", err)
	}
	m.stats.Buckets = 1

	fields := map[string]interface{}{
		"bucket": bucket, "objects_moved": m.stats.Objects, "versions_moved": m.stats.Versions, "marker_aliases": m.stats.MarkerAliases,
		"duplicates_removed": m.stats.Duplicates, "conflicts_kept": m.stats.Conflicts,
		"temp_files_skipped": m.stats.SkippedTemp, "unmappable_kept": m.stats.Unmappable,
	}
	if m.stats.leftovers() > 0 {
		fields["legacy_objects"] = legacyObjects
		fields["legacy_versions"] = legacyVersions
		logger.Warn("Local storage: bucket migrated with leftovers kept in its legacy directories (inspect them; interrupted-upload temp files are safe to delete)", fields)
	} else {
		logger.Info("Local storage: bucket migrated", fields)
	}
	return m.stats, nil
}

// walk visits every non-directory entry under dir (a missing dir is empty).
// Files are moved out while walking, which is safe: WalkDir reads a
// directory's entries before visiting them.
func (m *bucketMigrator) walk(dir string, visit func(dir, p string, d fs.DirEntry) error) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir && isNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		m.seen++
		if m.seen%migrationProgressEvery == 0 {
			logger.Info("Local storage: migration progress", map[string]interface{}{
				"bucket": m.bucket, "files_seen": m.seen, "objects_moved": m.stats.Objects, "versions_moved": m.stats.Versions,
			})
		}
		name := d.Name()
		if strings.HasPrefix(name, uploadTempPrefix) || strings.HasPrefix(name, assembleTempPrefix) {
			m.stats.SkippedTemp++
			return nil
		}
		if !d.Type().IsRegular() {
			m.stats.Unmappable++
			logger.Warn("Local storage migration: not a regular file, left in place", map[string]interface{}{"bucket": m.bucket, "path": strconv.Quote(p)})
			return nil
		}
		if err := visit(dir, p, d); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		return nil
	})
}

// legacyKeyForPath maps a legacy bucket-relative path ('/'-separated) to the
// object key it stored: "<dir>/.bkt-folder" is the folder marker "<dir>/";
// everything else is the key itself. A root-level ".bkt-folder" can only be
// an object of that name from before folder-marker support ("/" is not a key).
func legacyKeyForPath(rel string) string {
	if strings.HasSuffix(rel, "/"+legacyFolderMarkerName) {
		return strings.TrimSuffix(rel, legacyFolderMarkerName)
	}
	return rel
}

func (m *bucketMigrator) migrateObject(dir, p string, _ fs.DirEntry) error {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return err
	}
	key := legacyKeyForPath(filepath.ToSlash(rel))
	if err := m.aliasEmptyMarker(p, key, func(k string) (string, string, string) {
		l := objectLocIn(m.objRoot, k)
		return l.dir, l.blob, l.sidecar
	}); err != nil {
		return err
	}
	loc := objectLocIn(m.objRoot, key)
	return m.place(p, loc.dir, loc.blob, loc.sidecar, key, &m.stats.Objects)
}

func (m *bucketMigrator) migrateVersion(dir, p string, d fs.DirEntry) error {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return err
	}
	vid := d.Name()
	keyPath := filepath.ToSlash(filepath.Dir(rel))
	if !isCanonicalUUID(vid) || keyPath == "." {
		m.stats.Unmappable++
		logger.Warn("Local storage migration: not an archived version (bad version id or location), left in place", map[string]interface{}{"bucket": m.bucket, "path": strconv.Quote(p)})
		return nil
	}
	key := legacyKeyForPath(keyPath)
	if err := m.aliasEmptyMarker(p, key, func(k string) (string, string, string) {
		d := filepath.Join(m.verRoot, keyHash(k))
		return d, filepath.Join(d, vid), filepath.Join(d, versionSidecarName)
	}); err != nil {
		return err
	}
	vdir := filepath.Join(m.verRoot, keyHash(key))
	return m.place(p, vdir, filepath.Join(vdir, vid), filepath.Join(vdir, versionSidecarName), key, &m.stats.Versions)
}

// aliasEmptyMarker handles the one ambiguous legacy shape: a zero-length file
// "<dir>" may be the object "<dir>" or (older releases) the folder marker
// "<dir>/". The file itself is migrated as "<dir>"; this additionally creates
// an empty blob for "<dir>/" (never overwriting an existing one) so whichever
// key the database holds keeps working. locFor maps a key to its (dir, blob,
// sidecar) paths in the target tree.
func (m *bucketMigrator) aliasEmptyMarker(src, key string, locFor func(string) (string, string, string)) error {
	if strings.HasSuffix(key, "/") {
		return nil
	}
	fi, err := os.Lstat(src)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() != 0 {
		return err
	}
	aliasKey := key + "/"
	dir, blob, sidecar := locFor(aliasKey)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return err
	}
	if err := ensureSidecar(dir, sidecar, aliasKey); err != nil {
		return err
	}
	f, err := os.OpenFile(blob, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600) //nolint:gosec // blob is <root>/.objects or .objversions + sha256 fan-out, never key text
	if err != nil {
		if os.IsExist(err) {
			return nil // already present (earlier run or a real marker)
		}
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	m.stats.MarkerAliases++
	return nil
}

// place moves the legacy file src to dst (in dstDir, described by the key
// sidecar) without ever overwriting or losing bytes.
func (m *bucketMigrator) place(src, dstDir, dst, sidecar, key string, moved *int) error {
	if err := os.MkdirAll(dstDir, 0750); err != nil {
		return err
	}
	if err := ensureSidecar(dstDir, sidecar, key); err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil {
		same, err := sameFileContents(src, dst)
		if err != nil {
			return err
		}
		if same {
			if err := os.Remove(src); err != nil {
				return err
			}
			m.stats.Duplicates++
			return nil
		}
		m.stats.Conflicts++
		logger.Error("Local storage migration: CONFLICT — the blob layout already holds different bytes for this key; kept both (the blob-layout copy is served, the legacy file is left in place for manual review)", map[string]interface{}{
			"bucket": m.bucket, "key": strconv.Quote(key), "legacy_file": strconv.Quote(src), "blob": dst,
		})
		return nil
	} else if !isNotExist(err) {
		return err
	}
	if err := renameNoReplace(src, dst); err != nil {
		return err
	}
	*moved++
	return nil
}

// sameFileContents reports whether a and b hold identical bytes (trivially
// so when they are the same inode, e.g. after a crash between the link and
// the unlink of renameNoReplace).
func sameFileContents(a, b string) (bool, error) {
	ai, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	if os.SameFile(ai, bi) {
		return true, nil
	}
	if ai.Size() != bi.Size() {
		return false, nil
	}
	fa, err := os.Open(a) //nolint:gosec // storage-internal paths from walking the storage root
	if err != nil {
		return false, err
	}
	defer fa.Close()      //nolint:errcheck // read-only
	fb, err := os.Open(b) //nolint:gosec // storage-internal paths from walking the storage root
	if err != nil {
		return false, err
	}
	defer fb.Close() //nolint:errcheck // read-only
	bufA, bufB := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		na, ea := io.ReadFull(fa, bufA)
		nb, eb := io.ReadFull(fb, bufB)
		if na != nb || !bytes.Equal(bufA[:na], bufB[:nb]) {
			return false, nil
		}
		aDone := ea == io.EOF || ea == io.ErrUnexpectedEOF
		bDone := eb == io.EOF || eb == io.ErrUnexpectedEOF
		if ea != nil && !aDone {
			return false, ea
		}
		if eb != nil && !bDone {
			return false, eb
		}
		if aDone || bDone {
			return aDone == bDone, nil
		}
	}
}

// removeEmptyDirs removes the empty directories of the tree at dir, deepest
// first, including dir itself once empty. rmdir(2) never removes a directory
// that still holds anything, so leftovers keep their directories.
func removeEmptyDirs(dir string) {
	var dirs []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	// WalkDir visits parents before children: reverse = children first.
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = syscall.Rmdir(dirs[i])
	}
}
