package storage

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3Storage implements StorageBackend using S3-compatible storage
type S3Storage struct {
	client       *s3.Client
	bucketPrefix string
}

// NewS3Storage creates a new S3 storage backend
func NewS3Storage(endpoint, region, accessKeyID, secretAccessKey, bucketPrefix string, useSSL, forcePathStyle bool) (*S3Storage, error) {
	// Create custom endpoint resolver for S3-compatible services
	customResolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		if endpoint != "" && endpoint != "s3.amazonaws.com" {
			scheme := "https"
			if !useSSL {
				scheme = "http"
			}
			return aws.Endpoint{
				URL:               fmt.Sprintf("%s://%s", scheme, endpoint),
				HostnameImmutable: true,
				Source:            aws.EndpointSourceCustom,
			}, nil
		}
		// Use default AWS endpoint
		return aws.Endpoint{}, &aws.EndpointNotFoundError{}
	})

	// Load AWS configuration
	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion(region),
		config.WithEndpointResolverWithOptions(customResolver),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			accessKeyID,
			secretAccessKey,
			"",
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Create S3 client
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = forcePathStyle
	})

	return &S3Storage{
		client:       client,
		bucketPrefix: bucketPrefix,
	}, nil
}

// getBucketName adds prefix to bucket name if configured
func (s3s *S3Storage) getBucketName(bucketName string) string {
	if s3s.bucketPrefix != "" {
		return fmt.Sprintf("%s-%s", s3s.bucketPrefix, bucketName)
	}
	return bucketName
}

// CreateBucket creates a new bucket in S3
func (s3s *S3Storage) CreateBucket(bucketName, region string) error {
	ctx := context.Background()
	actualBucketName := s3s.getBucketName(bucketName)

	// Check if bucket already exists
	_, err := s3s.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(actualBucketName),
	})
	if err == nil {
		// Bucket already exists, that's fine
		return nil
	}

	// Create the bucket
	createInput := &s3.CreateBucketInput{
		Bucket: aws.String(actualBucketName),
	}

	// For regions other than us-east-1, we need to specify LocationConstraint
	if region != "" && region != "us-east-1" {
		createInput.CreateBucketConfiguration = &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(region),
		}
	}

	_, err = s3s.client.CreateBucket(ctx, createInput)
	if err != nil {
		return fmt.Errorf("failed to create S3 bucket: %w", err)
	}

	return nil
}

// DeleteBucket removes a bucket from S3 (bucket must be empty)
func (s3s *S3Storage) DeleteBucket(bucketName string) error {
	ctx := context.Background()
	actualBucketName := s3s.getBucketName(bucketName)

	_, err := s3s.client.DeleteBucket(ctx, &s3.DeleteBucketInput{
		Bucket: aws.String(actualBucketName),
	})
	if err != nil {
		return fmt.Errorf("failed to delete S3 bucket: %w", err)
	}

	return nil
}

// PutObject stores an object in S3
func (s3s *S3Storage) PutObject(bucketName, objectKey string, data io.Reader, size int64, contentType string) error {
	ctx := context.Background()
	actualBucketName := s3s.getBucketName(bucketName)

	// Ensure bucket exists
	_, err := s3s.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(actualBucketName),
	})
	if err != nil {
		// Bucket doesn't exist, attempt to create it
		_, err = s3s.client.CreateBucket(ctx, &s3.CreateBucketInput{
			Bucket: aws.String(actualBucketName),
		})
		if err != nil {
			// Categorize error for better debugging
			errMsg := err.Error()
			if strings.Contains(errMsg, "BucketAlreadyOwnedByYou") {
				// Bucket exists but HeadBucket failed (eventual consistency) - safe to proceed
				// Don't return error, continue with upload
			} else if strings.Contains(errMsg, "BucketAlreadyExists") {
				// Bucket owned by someone else - permission error
				return fmt.Errorf("bucket already exists (owned by another account): %w", err)
			} else if strings.Contains(errMsg, "AccessDenied") || strings.Contains(errMsg, "Forbidden") {
				// Permission error - clearly indicate auth failure
				return fmt.Errorf("insufficient permissions to create bucket (check S3 credentials): %w", err)
			} else if strings.Contains(errMsg, "InvalidBucketName") {
				// Invalid bucket name format
				return fmt.Errorf("invalid bucket name format: %w", err)
			} else {
				// Generic error - include full context
				return fmt.Errorf("failed to create S3 bucket '%s': %w", actualBucketName, err)
			}
		}
	}

	// Upload object
	_, err = s3s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(actualBucketName),
		Key:           aws.String(objectKey),
		Body:          data,
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("failed to upload object: %w", err)
	}

	return nil
}

// GetObject retrieves an object from S3
func (s3s *S3Storage) GetObject(bucketName, objectKey string) (io.ReadCloser, error) {
	ctx := context.Background()
	actualBucketName := s3s.getBucketName(bucketName)

	result, err := s3s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(actualBucketName),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get object: %w", err)
	}

	return result.Body, nil
}

// DeleteObject removes an object from S3
func (s3s *S3Storage) DeleteObject(bucketName, objectKey string) error {
	ctx := context.Background()
	actualBucketName := s3s.getBucketName(bucketName)

	_, err := s3s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(actualBucketName),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return fmt.Errorf("failed to delete object: %w", err)
	}

	return nil
}

// ListObjects lists all objects in a bucket with the given prefix
func (s3s *S3Storage) ListObjects(bucketName, prefix string) ([]ObjectInfo, error) {
	ctx := context.Background()
	actualBucketName := s3s.getBucketName(bucketName)
	objects := make([]ObjectInfo, 0)

	// Check if bucket exists
	_, err := s3s.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(actualBucketName),
	})
	if err != nil {
		return objects, nil // Return empty list if bucket doesn't exist
	}

	// List objects
	paginator := s3.NewListObjectsV2Paginator(s3s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(actualBucketName),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list objects: %w", err)
		}

		for _, obj := range page.Contents {
			contentType := "application/octet-stream"
			
			// Get object metadata to retrieve content type
			headResult, err := s3s.client.HeadObject(ctx, &s3.HeadObjectInput{
				Bucket: aws.String(actualBucketName),
				Key:    obj.Key,
			})
			if err == nil && headResult.ContentType != nil {
				contentType = *headResult.ContentType
			}

			etag := ""
			if obj.ETag != nil {
				etag = strings.Trim(*obj.ETag, "\"")
			}

			objects = append(objects, ObjectInfo{
				Key:          *obj.Key,
				Size:         *obj.Size,
				ContentType:  contentType,
				LastModified: obj.LastModified.Format(time.RFC3339),
				ETag:         etag,
			})
		}
	}

	return objects, nil
}

// ObjectExists checks if an object exists in S3
func (s3s *S3Storage) ObjectExists(bucketName, objectKey string) (bool, error) {
	ctx := context.Background()
	actualBucketName := s3s.getBucketName(bucketName)

	_, err := s3s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(actualBucketName),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		// Check if error is "not found"
		var notFound *types.NotFound
		var noSuchKey *types.NoSuchKey
		if strings.Contains(err.Error(), "NotFound") || 
		   strings.Contains(err.Error(), "NoSuchKey") ||
		   err == notFound || err == noSuchKey {
			return false, nil
		}
		return false, fmt.Errorf("failed to check object: %w", err)
	}

	return true, nil
}

// GetObjectInfo gets metadata about an object
func (s3s *S3Storage) GetObjectInfo(bucketName, objectKey string) (*ObjectInfo, error) {
	ctx := context.Background()
	actualBucketName := s3s.getBucketName(bucketName)

	result, err := s3s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(actualBucketName),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get object info: %w", err)
	}

	contentType := "application/octet-stream"
	if result.ContentType != nil {
		contentType = *result.ContentType
	}

	etag := ""
	if result.ETag != nil {
		etag = strings.Trim(*result.ETag, "\"")
	}

	size := int64(0)
	if result.ContentLength != nil {
		size = *result.ContentLength
	}

	lastModified := ""
	if result.LastModified != nil {
		lastModified = result.LastModified.Format(time.RFC3339)
	}

	return &ObjectInfo{
		Key:          objectKey,
		Size:         size,
		ContentType:  contentType,
		LastModified: lastModified,
		ETag:         etag,
	}, nil
}

// CopyObject copies an object within the same bucket using S3 CopyObject API
func (s3s *S3Storage) CopyObject(bucketName, srcKey, dstKey string) error {
	ctx := context.Background()
	actualBucketName := s3s.getBucketName(bucketName)

	// CopySource format: bucket/key
	copySource := fmt.Sprintf("%s/%s", actualBucketName, srcKey)

	_, err := s3s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(actualBucketName),
		Key:        aws.String(dstKey),
		CopySource: aws.String(copySource),
	})
	if err != nil {
		return fmt.Errorf("failed to copy object: %w", err)
	}

	return nil
}
