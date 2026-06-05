package storage

import (
	"io"
	"time"
)

// StorageBackend defines the interface for object storage operations
type StorageBackend interface {
	// CreateBucket creates a new bucket in the storage backend
	CreateBucket(bucketName, region string) error

	// DeleteBucket removes a bucket from the storage backend (must be empty)
	DeleteBucket(bucketName string) error

	// BucketExists checks if a bucket exists and is accessible in the storage backend
	BucketExists(bucketName string) (bool, error)

	// PutObject stores an object in the given bucket
	PutObject(bucketName, objectKey string, data io.Reader, size int64, contentType string) error

	// GetObject retrieves an object from the given bucket
	GetObject(bucketName, objectKey string) (io.ReadCloser, error)

	// DeleteObject removes an object from the given bucket
	DeleteObject(bucketName, objectKey string) error

	// ListObjects lists all objects in a bucket with the given prefix
	ListObjects(bucketName, prefix string) ([]ObjectInfo, error)

	// ObjectExists checks if an object exists in a bucket
	ObjectExists(bucketName, objectKey string) (bool, error)

	// GetObjectInfo gets metadata about an object
	GetObjectInfo(bucketName, objectKey string) (*ObjectInfo, error)

	// CopyObject copies an object within the same bucket
	CopyObject(bucketName, srcKey, dstKey string) error

	// Multipart upload operations
	CreateMultipartUpload(bucketName, objectKey, contentType string) (uploadID string, err error)
	UploadPart(bucketName, objectKey, uploadID string, partNumber int, data io.Reader, size int64) (etag string, err error)
	CompleteMultipartUpload(bucketName, objectKey, uploadID string, parts []CompletedPart) error
	AbortMultipartUpload(bucketName, objectKey, uploadID string) error
	ListParts(bucketName, objectKey, uploadID string) ([]PartInfo, error)
}

// ObjectInfo contains metadata about a stored object
type ObjectInfo struct {
	Key          string
	Size         int64
	ContentType  string
	LastModified string
	ETag         string
}

// CompletedPart represents a completed multipart upload part
type CompletedPart struct {
	PartNumber int
	ETag       string
}

// PartInfo contains metadata about an uploaded part
type PartInfo struct {
	PartNumber   int
	Size         int64
	ETag         string
	LastModified time.Time
}

// NewStorageBackend creates a new storage backend based on configuration
func NewStorageBackend(backend string, rootPath string, s3Endpoint, s3Region, s3AccessKey, s3SecretKey, s3BucketPrefix string, s3UseSSL, s3ForcePathStyle bool) (StorageBackend, error) {
	switch backend {
	case "s3":
		return NewS3Storage(s3Endpoint, s3Region, s3AccessKey, s3SecretKey, s3BucketPrefix, s3UseSSL, s3ForcePathStyle)
	case "local":
		fallthrough
	default:
		return NewLocalStorage(rootPath), nil
	}
}
