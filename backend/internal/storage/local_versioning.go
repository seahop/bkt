package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// versionDir returns the containment-checked directory for a key's stored
// versions: <root>/.versions/<bucket>/<key>/. It lives OUTSIDE bucket
// directories so bucket listings and deletes never see version bytes.
func (ls *LocalStorage) versionPath(bucketName, objectKey, versionID string) (string, error) {
	if _, err := uuid.Parse(versionID); err != nil {
		return "", fmt.Errorf("invalid version id")
	}
	if err := checkLocalObjectKey(bucketName, objectKey); err != nil {
		return "", err
	}
	// Folder-marker keys ("a/b/") keep their versions under the marker name
	// ("a/b/.bkt-folder/<vid>"), apart from the versions of the key "a/b".
	return ls.resolve(".versions", bucketName, localKeyRel(objectKey), versionID)
}

// storedVersionPath is versionPath for an existing stored version. Releases
// before folder-marker support archived a marker key "a/b/" at the cleaned
// path ".versions/<bucket>/a/b/<vid>"; that legacy location is used when the
// version is not at the current one.
func (ls *LocalStorage) storedVersionPath(bucketName, objectKey, versionID string) (string, error) {
	p, err := ls.versionPath(bucketName, objectKey, versionID)
	if err != nil || !isFolderMarkerKey(objectKey) {
		return p, err
	}
	if _, serr := os.Lstat(p); os.IsNotExist(serr) {
		legacy, lerr := ls.resolve(".versions", bucketName, strings.TrimSuffix(objectKey, "/"), versionID)
		if lerr == nil {
			if info, ierr := os.Lstat(legacy); ierr == nil && info.Mode().IsRegular() {
				return legacy, nil
			}
		}
	}
	return p, nil
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
	src, err := ls.readablePath(bucketName, objectKey)
	if err != nil {
		return err
	}
	// Only an object's bytes are archived — never a folder that happens to
	// sit at the key's path (renaming it would move the folder's contents).
	if _, err := statObjectFile(src); err != nil {
		return fmt.Errorf("failed to archive version: object not found")
	}
	dst, err := ls.versionPath(bucketName, objectKey, versionID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0750); err != nil {
		return fmt.Errorf("failed to create version dir: %w", err)
	}
	if err := renameNoReplace(src, dst); err != nil {
		return fmt.Errorf("failed to archive version: %w", err)
	}
	return nil
}

func (ls *LocalStorage) PromoteObjectVersion(bucketName, objectKey, versionID string) error {
	src, err := ls.storedVersionPath(bucketName, objectKey, versionID)
	if err != nil {
		return err
	}
	dst, err := ls.objectPath(bucketName, objectKey)
	if err != nil {
		return err
	}
	ls.upgradeLegacyMarker(bucketName, objectKey)
	if err := os.MkdirAll(filepath.Dir(dst), 0750); err != nil {
		return fmt.Errorf("failed to create object dir: %w", err)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("failed to promote version: %w", err)
	}
	return nil
}

func (ls *LocalStorage) GetObjectVersion(bucketName, objectKey, versionID string) (io.ReadCloser, error) {
	p, err := ls.storedVersionPath(bucketName, objectKey, versionID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p) //nolint:gosec // path validated by versionPath()/resolve() containment
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("version not found")
		}
		return nil, fmt.Errorf("failed to open version: %w", err)
	}
	return f, nil
}

func (ls *LocalStorage) DeleteObjectVersion(bucketName, objectKey, versionID string) error {
	p, err := ls.storedVersionPath(bucketName, objectKey, versionID)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete version: %w", err)
	}
	return nil
}
