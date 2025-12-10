package storage

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LocalStorage implements StorageBackend using local filesystem
type LocalStorage struct {
	rootPath string
}

// NewLocalStorage creates a new local storage backend
func NewLocalStorage(rootPath string) *LocalStorage {
	return &LocalStorage{
		rootPath: rootPath,
	}
}

// CreateBucket creates a bucket directory in the local filesystem
func (ls *LocalStorage) CreateBucket(bucketName, region string) error {
	bucketPath := filepath.Join(ls.rootPath, bucketName)

	// Create the bucket directory
	if err := os.MkdirAll(bucketPath, 0755); err != nil {
		return fmt.Errorf("failed to create bucket directory: %w", err)
	}

	return nil
}

// PutObject stores an object in the local filesystem
func (ls *LocalStorage) PutObject(bucketName, objectKey string, data io.Reader, size int64, contentType string) error {
	bucketPath := filepath.Join(ls.rootPath, bucketName)
	objectPath := filepath.Join(bucketPath, objectKey)

	// Create directory if it doesn't exist
	dir := filepath.Dir(objectPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Create the file
	file, err := os.Create(objectPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	// Copy data to file
	_, err = io.Copy(file, data)
	if err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// GetObject retrieves an object from the local filesystem
func (ls *LocalStorage) GetObject(bucketName, objectKey string) (io.ReadCloser, error) {
	objectPath := filepath.Join(ls.rootPath, bucketName, objectKey)

	file, err := os.Open(objectPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("object not found")
		}
		return nil, fmt.Errorf("failed to open file: %w", err)
	}

	return file, nil
}

// DeleteObject removes an object from the local filesystem
func (ls *LocalStorage) DeleteObject(bucketName, objectKey string) error {
	objectPath := filepath.Join(ls.rootPath, bucketName, objectKey)

	err := os.Remove(objectPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete file: %w", err)
	}

	return nil
}

// ListObjects lists all objects in a bucket with the given prefix
func (ls *LocalStorage) ListObjects(bucketName, prefix string) ([]ObjectInfo, error) {
	bucketPath := filepath.Join(ls.rootPath, bucketName)
	objects := make([]ObjectInfo, 0)

	err := filepath.Walk(bucketPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Get relative path from bucket root
		relPath, err := filepath.Rel(bucketPath, path)
		if err != nil {
			return err
		}

		// Convert to forward slashes for consistency
		key := filepath.ToSlash(relPath)

		// Filter by prefix if provided
		if prefix != "" && !strings.HasPrefix(key, prefix) {
			return nil
		}

		// Calculate ETag (MD5 hash)
		etag, _ := calculateMD5(path)

		// Detect content type
		contentType := mime.TypeByExtension(filepath.Ext(path))
		if contentType == "" {
			contentType = "application/octet-stream"
		}

		objects = append(objects, ObjectInfo{
			Key:          key,
			Size:         info.Size(),
			ContentType:  contentType,
			LastModified: info.ModTime().Format(time.RFC3339),
			ETag:         etag,
		})

		return nil
	})

	if err != nil {
		if os.IsNotExist(err) {
			return objects, nil // Return empty list if bucket doesn't exist
		}
		return nil, fmt.Errorf("failed to list objects: %w", err)
	}

	return objects, nil
}

// ObjectExists checks if an object exists in a bucket
func (ls *LocalStorage) ObjectExists(bucketName, objectKey string) (bool, error) {
	objectPath := filepath.Join(ls.rootPath, bucketName, objectKey)

	_, err := os.Stat(objectPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check object: %w", err)
	}

	return true, nil
}

// GetObjectInfo gets metadata about an object
func (ls *LocalStorage) GetObjectInfo(bucketName, objectKey string) (*ObjectInfo, error) {
	objectPath := filepath.Join(ls.rootPath, bucketName, objectKey)

	info, err := os.Stat(objectPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("object not found")
		}
		return nil, fmt.Errorf("failed to get object info: %w", err)
	}

	// Calculate ETag (MD5 hash)
	etag, _ := calculateMD5(objectPath)

	// Detect content type
	contentType := mime.TypeByExtension(filepath.Ext(objectPath))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	return &ObjectInfo{
		Key:          objectKey,
		Size:         info.Size(),
		ContentType:  contentType,
		LastModified: info.ModTime().Format(time.RFC3339),
		ETag:         etag,
	}, nil
}

// CopyObject copies an object within the same bucket
func (ls *LocalStorage) CopyObject(bucketName, srcKey, dstKey string) error {
	srcPath := filepath.Join(ls.rootPath, bucketName, srcKey)
	dstPath := filepath.Join(ls.rootPath, bucketName, dstKey)

	// Check source exists
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		return fmt.Errorf("source object not found")
	}

	// Create destination directory if needed
	dstDir := filepath.Dir(dstPath)
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Try rename first (atomic and efficient for same filesystem)
	if err := os.Rename(srcPath, dstPath); err == nil {
		return nil
	}

	// Fallback to copy if rename fails (e.g., cross-device)
	srcFile, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("failed to copy file: %w", err)
	}

	return nil
}

// calculateMD5 calculates the MD5 hash of a file
func calculateMD5(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := md5.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}
