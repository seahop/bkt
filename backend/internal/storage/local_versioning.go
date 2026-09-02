package storage

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// versionDir returns the containment-checked directory for a key's stored
// versions: <root>/.versions/<bucket>/<key>/. It lives OUTSIDE bucket
// directories so bucket listings and deletes never see version bytes.
func (ls *LocalStorage) versionPath(bucketName, objectKey, versionID string) (string, error) {
	if _, err := uuid.Parse(versionID); err != nil {
		return "", fmt.Errorf("invalid version id")
	}
	return ls.resolve(".versions", bucketName, objectKey, versionID)
}

func (ls *LocalStorage) ArchiveObjectVersion(bucketName, objectKey, versionID string) error {
	src, err := ls.resolve(bucketName, objectKey)
	if err != nil {
		return err
	}
	dst, err := ls.versionPath(bucketName, objectKey, versionID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0750); err != nil {
		return fmt.Errorf("failed to create version dir: %w", err)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("failed to archive version: %w", err)
	}
	return nil
}

func (ls *LocalStorage) PromoteObjectVersion(bucketName, objectKey, versionID string) error {
	src, err := ls.versionPath(bucketName, objectKey, versionID)
	if err != nil {
		return err
	}
	dst, err := ls.resolve(bucketName, objectKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0750); err != nil {
		return fmt.Errorf("failed to create object dir: %w", err)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("failed to promote version: %w", err)
	}
	return nil
}

func (ls *LocalStorage) GetObjectVersion(bucketName, objectKey, versionID string) (io.ReadCloser, error) {
	p, err := ls.versionPath(bucketName, objectKey, versionID)
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
	p, err := ls.versionPath(bucketName, objectKey, versionID)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete version: %w", err)
	}
	return nil
}
