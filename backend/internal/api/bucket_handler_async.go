package api

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"bkt/internal/database"
	"bkt/internal/logger"
	"bkt/internal/models"
	"bkt/internal/services"
	"bkt/internal/validation"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ProgressReader wraps an io.ReadSeeker and tracks upload progress in real-time
type ProgressReader struct {
	reader        io.ReadSeeker
	uploadID      uuid.UUID
	totalSize     int64
	bytesRead     int64
	lastUpdate    time.Time
	updateMutex   sync.Mutex
	minUpdateInterval time.Duration
}

// NewProgressReader creates a new progress tracking reader
func NewProgressReader(reader io.ReadSeeker, uploadID uuid.UUID, totalSize int64) *ProgressReader {
	return &ProgressReader{
		reader:            reader,
		uploadID:          uploadID,
		totalSize:         totalSize,
		bytesRead:         0,
		lastUpdate:        time.Now(),
		minUpdateInterval: 500 * time.Millisecond, // Update DB at most every 500ms
	}
}

// Read implements io.Reader and tracks progress
func (pr *ProgressReader) Read(p []byte) (n int, err error) {
	n, err = pr.reader.Read(p)
	if n > 0 {
		pr.updateMutex.Lock()
		pr.bytesRead += int64(n)

		// Update database periodically to avoid too many writes
		now := time.Now()
		if now.Sub(pr.lastUpdate) >= pr.minUpdateInterval {
			pr.lastUpdate = now

			// Update in database (non-blocking) with actual bytes uploaded
			// This gives smooth incremental progress as bytes are transferred
			go func(bytesUploaded int64) {
				database.DB.Model(&models.Upload{}).
					Where("id = ?", pr.uploadID).
					Update("uploaded_size", bytesUploaded)
			}(pr.bytesRead)
		}
		pr.updateMutex.Unlock()
	}
	return n, err
}

// Seek implements io.Seeker to support AWS SDK retries
func (pr *ProgressReader) Seek(offset int64, whence int) (int64, error) {
	pr.updateMutex.Lock()
	defer pr.updateMutex.Unlock()

	// Delegate seek to underlying reader
	pos, err := pr.reader.Seek(offset, whence)
	if err != nil {
		return pos, err
	}

	// Reset bytesRead to match the new position
	// This ensures progress tracking remains accurate after seeks
	pr.bytesRead = pos

	return pos, nil
}

// UploadObjectAsync initiates an asynchronous upload and returns immediately with upload ID
func (h *BucketHandler) UploadObjectAsync(c *gin.Context) {
	bucketName := c.Param("name")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	// Get bucket from database
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "Bucket not found",
		})
		return
	}

	// Get object key from form or query
	objectKey := c.PostForm("key")
	if objectKey == "" {
		objectKey = c.Query("key")
	}
	if objectKey == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Object key is required",
		})
		return
	}

	// Validate object key
	if err := validation.ValidateObjectKey(objectKey); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid object key",
			Message: err.Error(),
		})
		return
	}

	// Check policy permissions
	allowed, err := h.policyService.CheckObjectAccess(userUUID, bucketName, objectKey, services.ActionPutObject)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Policy check failed",
			Message: err.Error(),
		})
		return
	}
	if !allowed {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Permission denied",
			Message: "You don't have permission to upload objects to this bucket",
		})
		return
	}

	// Get uploaded file
	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Failed to get file",
			Message: err.Error(),
		})
		return
	}

	// Validate file size
	if fileHeader.Size < 0 {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid file size",
			Message: "File size cannot be negative",
		})
		return
	}

	if fileHeader.Size > h.config.Storage.MaxFileSize {
		c.JSON(http.StatusRequestEntityTooLarge, models.ErrorResponse{
			Error:   "File too large",
			Message: fmt.Sprintf("Maximum file size is %d bytes", h.config.Storage.MaxFileSize),
		})
		return
	}

	// Open uploaded file to detect content type
	file, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to open file",
			Message: err.Error(),
		})
		return
	}

	// Detect content type
	detectedType, _, err := validation.DetectContentType(file)
	file.Close()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to detect content type",
			Message: err.Error(),
		})
		return
	}

	// Validate content type
	if !validation.IsSafeContentType(detectedType) {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Forbidden file type",
			Message: fmt.Sprintf("File type '%s' is not allowed", detectedType),
		})
		return
	}

	// Create upload record
	upload := models.Upload{
		UserID:      userUUID,
		BucketName:  bucketName,
		ObjectKey:   objectKey,
		Filename:    fileHeader.Filename,
		ContentType: detectedType,
		TotalSize:   fileHeader.Size,
		Status:      models.UploadStatusPending,
	}

	if err := database.DB.Create(&upload).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to create upload record",
			Message: err.Error(),
		})
		return
	}

	// Save file to temporary location for background processing
	tempDir := filepath.Join(os.TempDir(), "bkt-uploads", upload.ID.String())
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to create temporary directory",
			Message: err.Error(),
		})
		return
	}

	tempFilePath := filepath.Join(tempDir, fileHeader.Filename)
	if err := c.SaveUploadedFile(fileHeader, tempFilePath); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to save uploaded file",
			Message: err.Error(),
		})
		return
	}

	// Start background upload processing
	go h.processAsyncUpload(upload.ID, tempFilePath, &bucket)

	// Return upload ID immediately
	c.JSON(http.StatusAccepted, gin.H{
		"upload_id": upload.ID,
		"status":    upload.Status,
		"message":   "Upload initiated. Use /api/uploads/" + upload.ID.String() + "/status to check progress.",
	})
}

// processAsyncUpload processes the upload in the background
func (h *BucketHandler) processAsyncUpload(uploadID uuid.UUID, tempFilePath string, bucket *models.Bucket) {
	// Ensure temp file is cleaned up
	defer func() {
		os.Remove(tempFilePath)
		os.Remove(filepath.Dir(tempFilePath)) // Remove temp directory
	}()

	// Get upload record
	var upload models.Upload
	if err := database.DB.First(&upload, uploadID).Error; err != nil {
		logger.Error("Failed to fetch upload record", map[string]interface{}{
			"upload_id": uploadID,
			"error":     err.Error(),
		})
		return
	}

	// Update status to processing
	upload.Status = models.UploadStatusProcessing
	upload.UploadedSize = 0 // Start at 0%
	database.DB.Save(&upload)

	// Open temp file
	file, err := os.Open(tempFilePath)
	if err != nil {
		upload.Status = models.UploadStatusFailed
		upload.ErrorMessage = fmt.Sprintf("Failed to open temporary file: %v", err)
		database.DB.Save(&upload)
		return
	}
	defer file.Close()

	// Re-detect content type from file
	detectedType, _, err := validation.DetectContentType(file)
	if err != nil {
		upload.Status = models.UploadStatusFailed
		upload.ErrorMessage = fmt.Sprintf("Failed to detect content type: %v", err)
		database.DB.Save(&upload)
		return
	}

	// Reset file position after reading (file is seekable so no need for MultiReader)
	file.Seek(0, 0)

	// Get storage backend
	storageBackend, err := h.getStorageBackend(bucket)
	if err != nil {
		upload.Status = models.UploadStatusFailed
		upload.ErrorMessage = fmt.Sprintf("Failed to initialize storage backend: %v", err)
		database.DB.Save(&upload)
		return
	}

	// Upload to storage with real-time progress tracking
	// ProgressReader will update uploaded_size as bytes are transferred
	startTime := time.Now()

	// Wrap file with progress tracker for real-time updates
	// File implements io.ReadSeeker, so ProgressReader will be seekable for AWS SDK retries
	progressReader := NewProgressReader(file, upload.ID, upload.TotalSize)

	if err := storageBackend.PutObject(bucket.Name, upload.ObjectKey, progressReader, upload.TotalSize, detectedType); err != nil {
		upload.Status = models.UploadStatusFailed
		upload.ErrorMessage = fmt.Sprintf("Failed to upload to storage: %v", err)
		database.DB.Save(&upload)
		return
	}
	// Upload complete - set to total size
	upload.UploadedSize = upload.TotalSize
	database.DB.Save(&upload)

	uploadDuration := time.Since(startTime)

	// Calculate SHA256 hash of the uploaded file
	file.Seek(0, 0)

	sha256Hash, err := validation.CalculateSHA256(file)
	if err != nil {
		logger.Warn("Failed to calculate SHA256 hash", map[string]interface{}{
			"upload_id": uploadID,
			"error":     err.Error(),
		})
		sha256Hash = "" // Continue without hash
	}

	// Calculate ETag (MD5)
	file.Seek(0, 0)

	etag, err := validation.CalculateMD5(file)
	if err != nil {
		logger.Warn("Failed to calculate ETag", map[string]interface{}{
			"upload_id": uploadID,
			"error":     err.Error(),
		})
		etag = ""
	}

	// Create object record in database
	storagePath := filepath.Join(bucket.Name, upload.ObjectKey)
	if bucket.StorageBackend == "s3" {
		storagePath = fmt.Sprintf("s3://%s/%s", bucket.Name, upload.ObjectKey)
	}

	object := models.Object{
		BucketID:    bucket.ID,
		Key:         upload.ObjectKey,
		Size:        upload.TotalSize,
		ContentType: detectedType,
		ETag:        etag,
		SHA256:      sha256Hash,
		StoragePath: storagePath,
	}

	if err := database.DB.Create(&object).Error; err != nil {
		upload.Status = models.UploadStatusFailed
		upload.ErrorMessage = fmt.Sprintf("Failed to create object record: %v", err)
		database.DB.Save(&upload)
		return
	}

	// Update upload status to completed
	now := time.Now()
	upload.Status = models.UploadStatusCompleted
	upload.UploadedSize = upload.TotalSize
	upload.CompletedAt = &now
	upload.ObjectID = &object.ID
	database.DB.Save(&upload)

	logger.Info("Async upload completed", map[string]interface{}{
		"upload_id":      uploadID,
		"object_id":      object.ID,
		"size_bytes":     upload.TotalSize,
		"duration":       uploadDuration.String(),
		"average_speed":  fmt.Sprintf("%.2f MB/s", float64(upload.TotalSize)/(1024*1024)/uploadDuration.Seconds()),
	})
}

// GetUploadStatus returns the current status of an upload
func (h *BucketHandler) GetUploadStatus(c *gin.Context) {
	uploadIDStr := c.Param("id")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	uploadID, err := uuid.Parse(uploadIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Invalid upload ID",
		})
		return
	}

	// Get upload record
	var upload models.Upload
	if err := database.DB.Where("id = ? AND user_id = ?", uploadID, userUUID).First(&upload).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "Upload not found",
		})
		return
	}

	// Calculate progress percentage
	progressPct := 0.0
	if upload.TotalSize > 0 {
		progressPct = float64(upload.UploadedSize) / float64(upload.TotalSize) * 100
	}

	// Build response
	response := models.UploadStatusResponse{
		ID:           upload.ID,
		Status:       upload.Status,
		Filename:     upload.Filename,
		ObjectKey:    upload.ObjectKey,
		TotalSize:    upload.TotalSize,
		UploadedSize: upload.UploadedSize,
		ProgressPct:  progressPct,
		ErrorMessage: upload.ErrorMessage,
		ObjectID:     upload.ObjectID,
		CreatedAt:    upload.CreatedAt,
		CompletedAt:  upload.CompletedAt,
	}

	c.JSON(http.StatusOK, response)
}

// ListUploads returns all uploads for the current user
func (h *BucketHandler) ListUploads(c *gin.Context) {
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	// Optional query parameters for filtering
	status := c.Query("status") // e.g., "pending", "processing", "completed", "failed"
	limit := 50                  // Default limit
	if limitStr := c.Query("limit"); limitStr != "" {
		fmt.Sscanf(limitStr, "%d", &limit)
		if limit > 100 {
			limit = 100 // Max 100 results
		}
	}

	query := database.DB.Where("user_id = ?", userUUID)
	if status != "" {
		query = query.Where("status = ?", status)
	}

	var uploads []models.Upload
	if err := query.Order("created_at DESC").Limit(limit).Find(&uploads).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to fetch uploads",
			Message: err.Error(),
		})
		return
	}

	// Convert to response format
	responses := make([]models.UploadStatusResponse, len(uploads))
	for i, upload := range uploads {
		progressPct := 0.0
		if upload.TotalSize > 0 {
			progressPct = float64(upload.UploadedSize) / float64(upload.TotalSize) * 100
		}

		responses[i] = models.UploadStatusResponse{
			ID:           upload.ID,
			Status:       upload.Status,
			Filename:     upload.Filename,
			ObjectKey:    upload.ObjectKey,
			TotalSize:    upload.TotalSize,
			UploadedSize: upload.UploadedSize,
			ProgressPct:  progressPct,
			ErrorMessage: upload.ErrorMessage,
			ObjectID:     upload.ObjectID,
			CreatedAt:    upload.CreatedAt,
			CompletedAt:  upload.CompletedAt,
		}
	}

	c.JSON(http.StatusOK, responses)
}
