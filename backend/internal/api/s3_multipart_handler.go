package api

import (
	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/services"
	"bkt/internal/storage"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Multipart XML types

type InitiateMultipartUploadResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	Xmlns    string   `xml:"xmlns,attr"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadId string   `xml:"UploadId"`
}

type CompleteMultipartUploadRequest struct {
	XMLName xml.Name      `xml:"CompleteMultipartUpload"`
	Parts   []PartElement `xml:"Part"`
}

type PartElement struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

type CompleteMultipartUploadResult struct {
	XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
	Xmlns    string   `xml:"xmlns,attr"`
	Location string   `xml:"Location"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	ETag     string   `xml:"ETag"`
}

type ListPartsResult struct {
	XMLName              xml.Name        `xml:"ListPartsResult"`
	Xmlns                string          `xml:"xmlns,attr"`
	Bucket               string          `xml:"Bucket"`
	Key                  string          `xml:"Key"`
	UploadId             string          `xml:"UploadId"`
	StorageClass         string          `xml:"StorageClass"`
	IsTruncated          bool            `xml:"IsTruncated"`
	Parts                []ListPartEntry `xml:"Part"`
}

type ListPartEntry struct {
	PartNumber   int       `xml:"PartNumber"`
	LastModified time.Time `xml:"LastModified"`
	ETag         string    `xml:"ETag"`
	Size         int64     `xml:"Size"`
}

// HandleObjectPost dispatches POST /{bucket}/{key} based on query params
func (h *S3APIHandler) HandleObjectPost(c *gin.Context) {
	if _, exists := c.GetQuery("uploads"); exists {
		h.CreateMultipartUpload(c)
		return
	}
	if uploadID := c.Query("uploadId"); uploadID != "" {
		h.CompleteMultipartUpload(c)
		return
	}
	h.s3Error(c, "NotImplemented", "This POST operation is not implemented", "", http.StatusNotImplemented)
}

// CreateMultipartUpload handles POST /{bucket}/{key}?uploads
func (h *S3APIHandler) CreateMultipartUpload(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := strings.TrimPrefix(c.Param("key"), "/")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	if allowed, _ := h.policyService.CheckObjectAccess(userUUID, bucketName, objectKey, services.ActionPutObject); !allowed {
		h.s3Error(c, "AccessDenied", "Access Denied", objectKey, http.StatusForbidden)
		return
	}

	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		h.s3Error(c, "NoSuchBucket", "The specified bucket does not exist", bucketName, http.StatusNotFound)
		return
	}

	contentType := c.GetHeader("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	storageBackend, err := h.bucketHandler.getStorageBackend(&bucket)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to initialize storage", objectKey, http.StatusInternalServerError)
		return
	}

	uploadID, err := storageBackend.CreateMultipartUpload(bucketName, objectKey, contentType)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to create multipart upload", objectKey, http.StatusInternalServerError)
		return
	}

	// Track in database for cleanup purposes
	mpu := models.MultipartUpload{
		UploadID:    uploadID,
		UserID:      userUUID,
		BucketName:  bucketName,
		ObjectKey:   objectKey,
		ContentType: contentType,
		Status:      "in-progress",
		ExpiresAt:   time.Now().Add(7 * 24 * time.Hour),
	}
	database.DB.Create(&mpu)

	c.Header("x-amz-request-id", uuid.New().String())
	c.XML(http.StatusOK, InitiateMultipartUploadResult{
		Xmlns:    "http://s3.amazonaws.com/doc/2006-03-01/",
		Bucket:   bucketName,
		Key:      objectKey,
		UploadId: uploadID,
	})
}

// UploadPart handles PUT /{bucket}/{key}?partNumber=N&uploadId=X
func (h *S3APIHandler) UploadPart(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := strings.TrimPrefix(c.Param("key"), "/")
	uploadID := c.Query("uploadId")
	partNumberStr := c.Query("partNumber")

	partNumber, err := strconv.Atoi(partNumberStr)
	if err != nil || partNumber < 1 || partNumber > 10000 {
		h.s3Error(c, "InvalidArgument", "Invalid part number", objectKey, http.StatusBadRequest)
		return
	}

	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		h.s3Error(c, "NoSuchBucket", "The specified bucket does not exist", bucketName, http.StatusNotFound)
		return
	}

	storageBackend, err := h.bucketHandler.getStorageBackend(&bucket)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to initialize storage", objectKey, http.StatusInternalServerError)
		return
	}

	size := c.Request.ContentLength
	if size < 0 {
		if v := c.GetHeader("X-Amz-Decoded-Content-Length"); v != "" {
			size, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	bodyReader := io.Reader(c.Request.Body)
	if c.GetHeader("Content-Encoding") == "aws-chunked" {
		bodyReader = newAWSChunkedReader(c.Request.Body)
	}
	etag, err := storageBackend.UploadPart(bucketName, objectKey, uploadID, partNumber, bodyReader, size)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to upload part", objectKey, http.StatusInternalServerError)
		return
	}

	c.Header("ETag", fmt.Sprintf(`"%s"`, etag))
	c.Header("x-amz-request-id", uuid.New().String())
	c.Status(http.StatusOK)
}

// CompleteMultipartUpload handles POST /{bucket}/{key}?uploadId=X
func (h *S3APIHandler) CompleteMultipartUpload(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := strings.TrimPrefix(c.Param("key"), "/")
	uploadID := c.Query("uploadId")

	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 4<<20))
	if err != nil {
		h.s3Error(c, "MalformedXML", "Failed to read request body", "", http.StatusBadRequest)
		return
	}

	var req CompleteMultipartUploadRequest
	if err := xml.Unmarshal(body, &req); err != nil {
		h.s3Error(c, "MalformedXML", "Invalid complete multipart XML", "", http.StatusBadRequest)
		return
	}

	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		h.s3Error(c, "NoSuchBucket", "The specified bucket does not exist", bucketName, http.StatusNotFound)
		return
	}

	storageBackend, err := h.bucketHandler.getStorageBackend(&bucket)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to initialize storage", objectKey, http.StatusInternalServerError)
		return
	}

	parts := make([]storage.CompletedPart, len(req.Parts))
	for i, p := range req.Parts {
		parts[i] = storage.CompletedPart{PartNumber: p.PartNumber, ETag: strings.Trim(p.ETag, `"`)}
	}

	if err := storageBackend.CompleteMultipartUpload(bucketName, objectKey, uploadID, parts); err != nil {
		h.s3Error(c, "InternalError", "Failed to complete multipart upload", objectKey, http.StatusInternalServerError)
		return
	}

	// Update DB metadata
	objInfo, _ := storageBackend.GetObjectInfo(bucketName, objectKey)
	if objInfo != nil {
		var existing models.Object
		if database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, objectKey).First(&existing).Error == nil {
			existing.Size = objInfo.Size
			existing.ETag = objInfo.ETag
			database.DB.Save(&existing)
		} else {
			var mpu models.MultipartUpload
			database.DB.Where("upload_id = ?", uploadID).First(&mpu)
			database.DB.Create(&models.Object{
				BucketID:    bucket.ID,
				Key:         objectKey,
				Size:        objInfo.Size,
				ContentType: mpu.ContentType,
				ETag:        objInfo.ETag,
				StoragePath: objectKey,
			})
		}
	}

	// Mark multipart upload completed
	database.DB.Model(&models.MultipartUpload{}).Where("upload_id = ?", uploadID).Update("status", "completed")

	c.Header("x-amz-request-id", uuid.New().String())
	c.XML(http.StatusOK, CompleteMultipartUploadResult{
		Xmlns:    "http://s3.amazonaws.com/doc/2006-03-01/",
		Location: fmt.Sprintf("/%s/%s", bucketName, objectKey),
		Bucket:   bucketName,
		Key:      objectKey,
		ETag:     objInfo.ETag,
	})
}

// AbortMultipartUpload handles DELETE /{bucket}/{key}?uploadId=X
func (h *S3APIHandler) AbortMultipartUpload(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := strings.TrimPrefix(c.Param("key"), "/")
	uploadID := c.Query("uploadId")

	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		h.s3Error(c, "NoSuchBucket", "The specified bucket does not exist", bucketName, http.StatusNotFound)
		return
	}

	storageBackend, err := h.bucketHandler.getStorageBackend(&bucket)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to initialize storage", objectKey, http.StatusInternalServerError)
		return
	}

	if err := storageBackend.AbortMultipartUpload(bucketName, objectKey, uploadID); err != nil {
		h.s3Error(c, "InternalError", "Failed to abort multipart upload", objectKey, http.StatusInternalServerError)
		return
	}

	database.DB.Model(&models.MultipartUpload{}).Where("upload_id = ?", uploadID).Update("status", "aborted")

	c.Header("x-amz-request-id", uuid.New().String())
	c.Status(http.StatusNoContent)
}

// ListPartsHandler handles GET /{bucket}/{key}?uploadId=X
func (h *S3APIHandler) ListPartsHandler(c *gin.Context) {
	bucketName := c.Param("bucket")
	objectKey := strings.TrimPrefix(c.Param("key"), "/")
	uploadID := c.Query("uploadId")

	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		h.s3Error(c, "NoSuchBucket", "The specified bucket does not exist", bucketName, http.StatusNotFound)
		return
	}

	storageBackend, err := h.bucketHandler.getStorageBackend(&bucket)
	if err != nil {
		h.s3Error(c, "InternalError", "Failed to initialize storage", objectKey, http.StatusInternalServerError)
		return
	}

	parts, err := storageBackend.ListParts(bucketName, objectKey, uploadID)
	if err != nil {
		h.s3Error(c, "NoSuchUpload", "The specified upload does not exist", uploadID, http.StatusNotFound)
		return
	}

	entries := make([]ListPartEntry, len(parts))
	for i, p := range parts {
		entries[i] = ListPartEntry{
			PartNumber:   p.PartNumber,
			LastModified: p.LastModified,
			ETag:         fmt.Sprintf(`"%s"`, p.ETag),
			Size:         p.Size,
		}
	}

	c.Header("x-amz-request-id", uuid.New().String())
	c.XML(http.StatusOK, ListPartsResult{
		Xmlns:        "http://s3.amazonaws.com/doc/2006-03-01/",
		Bucket:       bucketName,
		Key:          objectKey,
		UploadId:     uploadID,
		StorageClass: "STANDARD",
		IsTruncated:  false,
		Parts:        entries,
	})
}
