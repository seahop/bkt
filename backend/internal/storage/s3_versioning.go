package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

// s3VersionPrefix is where bkt-managed versions live inside the real bucket.
// It is excluded from ListObjects so version bytes never appear as objects.
const s3VersionPrefix = ".bkt-versions/"

func s3VersionKey(objectKey, versionID string) (string, error) {
	if _, err := uuid.Parse(versionID); err != nil {
		return "", fmt.Errorf("invalid version id")
	}
	return s3VersionPrefix + objectKey + "/" + versionID, nil
}

// serverSideMove copies srcKey to dstKey within the bucket and deletes srcKey.
func (s3s *S3Storage) serverSideMove(bucketName, srcKey, dstKey string) error {
	ctx := context.Background()
	actual := s3s.getBucketName(bucketName)
	_, err := s3s.client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(actual),
		Key:        aws.String(dstKey),
		CopySource: aws.String(url.PathEscape(actual + "/" + srcKey)),
	})
	if err != nil {
		return fmt.Errorf("failed to copy for version move: %w", err)
	}
	_, err = s3s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(actual),
		Key:    aws.String(srcKey),
	})
	if err != nil {
		return fmt.Errorf("failed to remove source after version move: %w", err)
	}
	return nil
}

func (s3s *S3Storage) ArchiveObjectVersion(bucketName, objectKey, versionID string) error {
	vk, err := s3VersionKey(objectKey, versionID)
	if err != nil {
		return err
	}
	return s3s.serverSideMove(bucketName, objectKey, vk)
}

func (s3s *S3Storage) PromoteObjectVersion(bucketName, objectKey, versionID string) error {
	vk, err := s3VersionKey(objectKey, versionID)
	if err != nil {
		return err
	}
	return s3s.serverSideMove(bucketName, vk, objectKey)
}

func (s3s *S3Storage) GetObjectVersion(bucketName, objectKey, versionID string) (io.ReadCloser, error) {
	vk, err := s3VersionKey(objectKey, versionID)
	if err != nil {
		return nil, err
	}
	out, err := s3s.client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(s3s.getBucketName(bucketName)),
		Key:    aws.String(vk),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get version: %w", err)
	}
	return out.Body, nil
}

func (s3s *S3Storage) DeleteObjectVersion(bucketName, objectKey, versionID string) error {
	vk, err := s3VersionKey(objectKey, versionID)
	if err != nil {
		return err
	}
	_, err = s3s.client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
		Bucket: aws.String(s3s.getBucketName(bucketName)),
		Key:    aws.String(vk),
	})
	if err != nil {
		return fmt.Errorf("failed to delete version: %w", err)
	}
	return nil
}
