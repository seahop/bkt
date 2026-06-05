package storage

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
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

// DeleteBucket removes a bucket directory from the local filesystem
func (ls *LocalStorage) DeleteBucket(bucketName string) error {
	bucketPath := filepath.Join(ls.rootPath, bucketName)

	// Remove the bucket directory and all contents
	if err := os.RemoveAll(bucketPath); err != nil {
		return fmt.Errorf("failed to delete bucket directory: %w", err)
	}

	return nil
}

// BucketExists checks if a bucket directory exists in the local filesystem
func (ls *LocalStorage) BucketExists(bucketName string) (bool, error) {
	bucketPath := filepath.Join(ls.rootPath, bucketName)

	info, err := os.Stat(bucketPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check bucket: %w", err)
	}

	// Ensure it's a directory
	return info.IsDir(), nil
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

		// Use mod time as ETag surrogate for listing (avoids expensive MD5 on every file)
		// Real ETag is computed on-demand via GetObjectInfo
		etag := fmt.Sprintf("%x-%x", info.ModTime().Unix(), info.Size())

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

	// CopyObject must preserve the source — always use io.Copy, never Rename
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

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

// multipartDir returns the temp directory path for a multipart upload
func (ls *LocalStorage) multipartDir(uploadID string) string {
	return filepath.Join(ls.rootPath, ".multipart", uploadID)
}

type multipartMeta struct {
	BucketName  string `json:"bucket_name"`
	ObjectKey   string `json:"object_key"`
	ContentType string `json:"content_type"`
}

func (ls *LocalStorage) CreateMultipartUpload(bucketName, objectKey, contentType string) (string, error) {
	uploadID := uuid.New().String()
	dir := ls.multipartDir(uploadID)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return "", fmt.Errorf("failed to create multipart dir: %w", err)
	}
	meta := multipartMeta{BucketName: bucketName, ObjectKey: objectKey, ContentType: contentType}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0600); err != nil {
		return "", fmt.Errorf("failed to write multipart meta: %w", err)
	}
	return uploadID, nil
}

func (ls *LocalStorage) UploadPart(bucketName, objectKey, uploadID string, partNumber int, data io.Reader, size int64) (string, error) {
	dir := ls.multipartDir(uploadID)
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("multipart upload not found: %s", uploadID)
	}
	partPath := filepath.Join(dir, fmt.Sprintf("part.%05d", partNumber))
	f, err := os.Create(partPath)
	if err != nil {
		return "", fmt.Errorf("failed to create part file: %w", err)
	}
	defer f.Close()

	hash := md5.New()
	w := io.MultiWriter(f, hash)
	if _, err := io.Copy(w, data); err != nil {
		return "", fmt.Errorf("failed to write part: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (ls *LocalStorage) CompleteMultipartUpload(bucketName, objectKey, uploadID string, parts []CompletedPart) error {
	dir := ls.multipartDir(uploadID)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("multipart upload not found: %s", uploadID)
	}

	// Sort parts by part number
	sorted := make([]CompletedPart, len(parts))
	copy(sorted, parts)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].PartNumber < sorted[j].PartNumber })

	finalPath := filepath.Join(ls.rootPath, bucketName, objectKey)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0750); err != nil {
		return fmt.Errorf("failed to create object dir: %w", err)
	}

	out, err := os.Create(finalPath)
	if err != nil {
		return fmt.Errorf("failed to create final object: %w", err)
	}
	defer out.Close()

	for _, part := range sorted {
		partPath := filepath.Join(dir, fmt.Sprintf("part.%05d", part.PartNumber))
		f, err := os.Open(partPath)
		if err != nil {
			return fmt.Errorf("failed to open part %d: %w", part.PartNumber, err)
		}
		_, copyErr := io.Copy(out, f)
		f.Close()
		if copyErr != nil {
			return fmt.Errorf("failed to assemble part %d: %w", part.PartNumber, copyErr)
		}
	}

	// Clean up temp dir
	os.RemoveAll(dir)
	return nil
}

func (ls *LocalStorage) AbortMultipartUpload(bucketName, objectKey, uploadID string) error {
	return os.RemoveAll(ls.multipartDir(uploadID))
}

func (ls *LocalStorage) ListParts(bucketName, objectKey, uploadID string) ([]PartInfo, error) {
	dir := ls.multipartDir(uploadID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("multipart upload not found: %s", uploadID)
	}

	var parts []PartInfo
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "part.") {
			continue
		}
		numStr := strings.TrimPrefix(entry.Name(), "part.")
		partNum, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}
		partPath := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}
		etag, _ := calculateMD5(partPath)
		parts = append(parts, PartInfo{
			PartNumber:   partNum,
			Size:         info.Size(),
			ETag:         etag,
			LastModified: info.ModTime(),
		})
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].PartNumber < parts[j].PartNumber })
	return parts, nil
}
