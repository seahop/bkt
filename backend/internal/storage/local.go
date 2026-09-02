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

// resolve joins the given path segments under the storage root and asserts the
// result stays inside the root. filepath.Join calls Clean, so ".." segments
// resolve *upward* rather than being rejected — without this containment check a
// crafted bucket/object/uploadID could read or write (or, via os.RemoveAll,
// delete) arbitrary paths on the host. Every filesystem sink in this backend
// must go through resolve.
func (ls *LocalStorage) resolve(parts ...string) (string, error) {
	for _, p := range parts {
		if p == "" {
			return "", fmt.Errorf("invalid empty path segment")
		}
		// Reject any ".." path element. This blocks not only escapes above the
		// storage root but also cross-bucket traversal (e.g. a key "../other/x"
		// that would otherwise land inside a sibling bucket's directory).
		for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
			if seg == ".." {
				return "", fmt.Errorf("path may not contain '..' segments")
			}
		}
	}
	joined := filepath.Join(append([]string{ls.rootPath}, parts...)...)
	rel, err := filepath.Rel(ls.rootPath, joined)
	if err != nil {
		return "", fmt.Errorf("invalid path")
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes storage root")
	}
	return joined, nil
}

// writeAtomic streams data into a temp file in dir and renames it into place
// only after a successful Sync+Close, so a failed or interrupted write never
// leaves a truncated object at the live key. It returns the hex MD5 of the
// bytes written (computed in the same pass — no second read).
func writeAtomic(dir, finalPath string, data io.Reader) (string, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-upload-*")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	hash := md5.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hash), data); err != nil {
		return "", fmt.Errorf("failed to write data: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return "", fmt.Errorf("failed to flush data: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("failed to close temp file: %w", err)
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		return "", fmt.Errorf("failed to commit object: %w", err)
	}
	committed = true
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// CreateBucket creates a bucket directory in the local filesystem
func (ls *LocalStorage) CreateBucket(bucketName, region string) error {
	bucketPath, err := ls.resolve(bucketName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(bucketPath, 0755); err != nil {
		return fmt.Errorf("failed to create bucket directory: %w", err)
	}
	return nil
}

// DeleteBucket removes a bucket directory from the local filesystem,
// including its archived version storage.
func (ls *LocalStorage) DeleteBucket(bucketName string) error {
	bucketPath, err := ls.resolve(bucketName)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(bucketPath); err != nil {
		return fmt.Errorf("failed to delete bucket directory: %w", err)
	}
	if verPath, verr := ls.resolve(".versions", bucketName); verr == nil {
		_ = os.RemoveAll(verPath)
	}
	return nil
}

// BucketExists checks if a bucket directory exists in the local filesystem
func (ls *LocalStorage) BucketExists(bucketName string) (bool, error) {
	bucketPath, err := ls.resolve(bucketName)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(bucketPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check bucket: %w", err)
	}
	return info.IsDir(), nil
}

// PutObject stores an object in the local filesystem atomically.
func (ls *LocalStorage) PutObject(bucketName, objectKey string, data io.Reader, size int64, contentType string, metadata map[string]string) error {
	_ = metadata // user metadata is served from the database for the local backend
	objectPath, err := ls.resolve(bucketName, objectKey)
	if err != nil {
		return err
	}
	if _, err := writeAtomic(filepath.Dir(objectPath), objectPath, data); err != nil {
		return err
	}
	return nil
}

// GetObject retrieves an object from the local filesystem
func (ls *LocalStorage) GetObject(bucketName, objectKey string) (io.ReadCloser, error) {
	objectPath, err := ls.resolve(bucketName, objectKey)
	if err != nil {
		return nil, err
	}
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
	objectPath, err := ls.resolve(bucketName, objectKey)
	if err != nil {
		return err
	}
	if err := os.Remove(objectPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete file: %w", err)
	}
	return nil
}

// ListObjects lists all objects in a bucket with the given prefix
func (ls *LocalStorage) ListObjects(bucketName, prefix string) ([]ObjectInfo, error) {
	bucketPath, err := ls.resolve(bucketName)
	if err != nil {
		return nil, err
	}
	objects := make([]ObjectInfo, 0)

	walkErr := filepath.Walk(bucketPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		relPath, err := filepath.Rel(bucketPath, path)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(relPath)

		// Skip internal temp files from interrupted atomic writes.
		if strings.HasPrefix(filepath.Base(key), ".tmp-upload-") {
			return nil
		}

		if prefix != "" && !strings.HasPrefix(key, prefix) {
			return nil
		}

		// Use mod time + size as an ETag surrogate for listing (avoids an
		// expensive MD5 on every file). The real content MD5 is computed
		// on-demand via GetObjectInfo.
		etag := fmt.Sprintf("%x-%x", info.ModTime().Unix(), info.Size())

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

	if walkErr != nil {
		if os.IsNotExist(walkErr) {
			return objects, nil // Empty list if bucket dir doesn't exist yet
		}
		return nil, fmt.Errorf("failed to list objects: %w", walkErr)
	}
	return objects, nil
}

// ObjectExists checks if an object exists in a bucket
func (ls *LocalStorage) ObjectExists(bucketName, objectKey string) (bool, error) {
	objectPath, err := ls.resolve(bucketName, objectKey)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(objectPath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check object: %w", err)
	}
	return true, nil
}

// GetObjectInfo gets metadata about an object
func (ls *LocalStorage) GetObjectInfo(bucketName, objectKey string) (*ObjectInfo, error) {
	objectPath, err := ls.resolve(bucketName, objectKey)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(objectPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("object not found")
		}
		return nil, fmt.Errorf("failed to get object info: %w", err)
	}

	etag, err := calculateMD5(objectPath)
	if err != nil {
		return nil, fmt.Errorf("failed to compute object etag: %w", err)
	}

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

// CopyObject copies an object within the same bucket.
func (ls *LocalStorage) CopyObject(bucketName, srcKey, dstKey string) error {
	srcPath, err := ls.resolve(bucketName, srcKey)
	if err != nil {
		return err
	}
	dstPath, err := ls.resolve(bucketName, dstKey)
	if err != nil {
		return err
	}

	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		return fmt.Errorf("source object not found")
	}

	// Copying an object onto itself (e.g. a metadata-only REPLACE copy) is a
	// no-op for the bytes. Short-circuit — otherwise the atomic write below
	// still handles it safely, but this avoids needless IO.
	if srcPath == dstPath {
		return nil
	}

	// Stream through a temp file + rename so the source is fully read before
	// the destination is committed. This is safe even if src and dst alias.
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer src.Close()

	if _, err := writeAtomic(filepath.Dir(dstPath), dstPath, src); err != nil {
		return err
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

// multipartDir returns the temp directory path for a multipart upload. The
// uploadID is validated as a UUID before it is ever used to build a path, so a
// crafted value (e.g. "../..") can never escape the multipart staging area —
// critical because AbortMultipartUpload calls os.RemoveAll on this path.
func (ls *LocalStorage) multipartDir(uploadID string) (string, error) {
	if _, err := uuid.Parse(uploadID); err != nil {
		return "", fmt.Errorf("invalid upload id")
	}
	return ls.resolve(".multipart", uploadID)
}

type multipartMeta struct {
	BucketName  string `json:"bucket_name"`
	ObjectKey   string `json:"object_key"`
	ContentType string `json:"content_type"`
}

func (ls *LocalStorage) CreateMultipartUpload(bucketName, objectKey, contentType string, metadata map[string]string) (string, error) {
	_ = metadata // applied from the tracking row at complete; DB is source of truth locally
	uploadID := uuid.New().String()
	dir, err := ls.multipartDir(uploadID)
	if err != nil {
		return "", err
	}
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
	dir, err := ls.multipartDir(uploadID)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(dir); err != nil {
		return "", fmt.Errorf("multipart upload not found: %s", uploadID)
	}
	partPath := filepath.Join(dir, fmt.Sprintf("part.%05d", partNumber))
	// Atomic part write: concurrent retries of the same part number can't
	// produce a torn file with a valid-looking MD5.
	etag, err := writeAtomic(dir, partPath, data)
	if err != nil {
		return "", err
	}
	return etag, nil
}

func (ls *LocalStorage) CompleteMultipartUpload(bucketName, objectKey, uploadID string, parts []CompletedPart) error {
	dir, err := ls.multipartDir(uploadID)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("multipart upload not found: %s", uploadID)
	}
	if len(parts) == 0 {
		return fmt.Errorf("cannot complete multipart upload with no parts")
	}

	sorted := make([]CompletedPart, len(parts))
	copy(sorted, parts)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].PartNumber < sorted[j].PartNumber })
	// A part number listed twice would be concatenated twice, silently
	// corrupting the assembled object (AWS rejects this as InvalidPartOrder).
	for i := 1; i < len(sorted); i++ {
		if sorted[i].PartNumber == sorted[i-1].PartNumber {
			return fmt.Errorf("duplicate part number %d in complete request", sorted[i].PartNumber)
		}
	}

	finalPath, err := ls.resolve(bucketName, objectKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0750); err != nil {
		return fmt.Errorf("failed to create object dir: %w", err)
	}

	// Assemble into a temp file in the object's directory, then rename into
	// place. A failure part-way through never leaves a partial object at the
	// live key.
	tmp, err := os.CreateTemp(filepath.Dir(finalPath), ".tmp-assemble-*")
	if err != nil {
		return fmt.Errorf("failed to create temp object: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()

	for _, part := range sorted {
		partPath := filepath.Join(dir, fmt.Sprintf("part.%05d", part.PartNumber))
		f, err := os.Open(partPath)
		if err != nil {
			return fmt.Errorf("failed to open part %d: %w", part.PartNumber, err)
		}
		_, copyErr := io.Copy(tmp, f)
		f.Close()
		if copyErr != nil {
			return fmt.Errorf("failed to assemble part %d: %w", part.PartNumber, copyErr)
		}
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("failed to flush assembled object: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close assembled object: %w", err)
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		return fmt.Errorf("failed to commit assembled object: %w", err)
	}
	committed = true

	_ = os.RemoveAll(dir)
	return nil
}

func (ls *LocalStorage) AbortMultipartUpload(bucketName, objectKey, uploadID string) error {
	dir, err := ls.multipartDir(uploadID)
	if err != nil {
		return err
	}
	// os.RemoveAll succeeds on a missing path; treat an unknown, well-formed
	// uploadID as a no-op success (matches S3 abort idempotency).
	return os.RemoveAll(dir)
}

func (ls *LocalStorage) ListParts(bucketName, objectKey, uploadID string) ([]PartInfo, error) {
	dir, err := ls.multipartDir(uploadID)
	if err != nil {
		return nil, err
	}
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
