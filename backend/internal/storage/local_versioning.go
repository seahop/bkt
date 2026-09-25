package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// versionLocation returns a key's version directory
// (<root>/.objversions/<bucket>/<sha256(key)>) and the path of one stored
// version in it. It lives OUTSIDE the bucket's object tree so object listings
// never see version bytes.
func (ls *LocalStorage) versionLocation(bucketName, objectKey, versionID string) (dir, file string, err error) {
	if !isCanonicalUUID(versionID) {
		return "", "", fmt.Errorf("invalid version id")
	}
	if err := checkObjectKey(objectKey); err != nil {
		return "", "", err
	}
	if err := checkBucketName(bucketName); err != nil {
		return "", "", err
	}
	dir = filepath.Join(ls.rootPath, objVersionsDirName, bucketName, keyHash(objectKey))
	return dir, filepath.Join(dir, versionID), nil
}

// renameNoReplace moves src to dst but fails if dst already exists, so an
// archived version can never be silently overwritten (e.g. by two racing
// overwrites that both archive the same current version id). os.Rename
// replaces an existing target on POSIX; a hard link + unlink gives an atomic
// "create only if absent" instead. Filesystems without hard-link support fall
// back to an existence check + rename (callers also serialize per key, so the
// remaining window is cross-process only).
func renameNoReplace(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		if rerr := os.Remove(src); rerr != nil {
			_ = os.Remove(dst)
			return rerr
		}
		return nil
	} else if os.IsExist(err) {
		return fmt.Errorf("archived version already exists")
	} else if _, serr := os.Lstat(src); serr != nil {
		return err // source missing / unreadable: report the real error
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("archived version already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(src, dst)
}

func (ls *LocalStorage) ArchiveObjectVersion(bucketName, objectKey, versionID string) error {
	loc, err := ls.objectLocation(bucketName, objectKey)
	if err != nil {
		return err
	}
	if _, err := statObject(loc.blob); err != nil {
		return fmt.Errorf("failed to archive version: object not found")
	}
	vdir, dst, err := ls.versionLocation(bucketName, objectKey, versionID)
	if err != nil {
		return err
	}
	sidecar := filepath.Join(vdir, versionSidecarName)
	// The version directory can be pruned by a concurrent version delete
	// between its creation and the move; withDirRetry re-creates it.
	if err := withDirRetry(vdir, func() error {
		if serr := ensureSidecar(vdir, sidecar, objectKey); serr != nil {
			return serr
		}
		if rerr := renameNoReplace(loc.blob, dst); rerr != nil {
			if _, serr := os.Lstat(loc.blob); isNotExist(rerr) && serr != nil {
				return fmt.Errorf("source vanished: %v", rerr) // not a directory race: don't retry
			}
			return rerr
		}
		return nil
	}); err != nil {
		pruneVersionDir(vdir, objectKey) // don't leave a fresh, empty version dir behind
		return fmt.Errorf("failed to archive version: %w", err)
	}
	// A racing prune may have removed the sidecar just before the move.
	_ = ensureSidecar(vdir, sidecar, objectKey)
	// The object's bytes moved out: its sidecar goes too.
	dropSidecarUnlessBlob(loc, objectKey)
	return nil
}

func (ls *LocalStorage) PromoteObjectVersion(bucketName, objectKey, versionID string) error {
	vdir, src, err := ls.versionLocation(bucketName, objectKey, versionID)
	if err != nil {
		return err
	}
	loc, err := ls.objectLocation(bucketName, objectKey)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(src); err != nil {
		if isNotExist(err) {
			return fmt.Errorf("failed to promote version: version not found")
		}
		return fmt.Errorf("failed to promote version: %w", err)
	}
	if err := prepareObject(loc, objectKey); err != nil {
		return err
	}
	if err := os.Rename(src, loc.blob); err != nil {
		dropSidecarUnlessBlob(loc, objectKey)
		if isNotExist(err) {
			return fmt.Errorf("failed to promote version: version not found")
		}
		return fmt.Errorf("failed to promote version: %w", err)
	}
	if err := finishObject(loc, objectKey); err != nil {
		return err
	}
	// The version's bytes left version storage.
	pruneVersionDir(vdir, objectKey)
	return nil
}

func (ls *LocalStorage) GetObjectVersion(bucketName, objectKey, versionID string) (io.ReadCloser, error) {
	_, p, err := ls.versionLocation(bucketName, objectKey, versionID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p) //nolint:gosec // <root>/.objversions/<bucket>/<sha256>/<canonical uuid>, never derived from key text
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("version not found")
		}
		return nil, fmt.Errorf("failed to open version: %w", err)
	}
	return f, nil
}

func (ls *LocalStorage) DeleteObjectVersion(bucketName, objectKey, versionID string) error {
	vdir, p, err := ls.versionLocation(bucketName, objectKey, versionID)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete version: %w", err)
	}
	pruneVersionDir(vdir, objectKey)
	return nil
}
