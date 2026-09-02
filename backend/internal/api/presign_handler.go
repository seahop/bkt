package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/security"
	"bkt/internal/services"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	presignDefaultExpiry = 15 * time.Minute
	presignMaxExpiry     = 7 * 24 * time.Hour // SigV4 hard limit
)

type presignRequest struct {
	Key       string `json:"key" binding:"required"`
	ExpiresIn int    `json:"expires_in"` // seconds; default 900, capped at 604800
}

// PresignObject issues a time-limited presigned GET URL for an object, signed
// with one of the caller's own access keys so the existing SigV4 verifier on
// the S3 listener validates it like any client-generated presign. Using the
// AWS SDK's presigner guarantees AWS-standard canonicalization — the same
// shape `aws s3 presign` produces, which the verifier already accepts.
// @Summary Generate a presigned download URL
// @Description Generates a time-limited presigned GET URL for an object, signed with one of the caller's active access keys. Requires GetObject permission and at least one active access key.
// @Tags buckets
// @Accept json
// @Produce json
// @Param name path string true "Bucket name"
// @Param request body presignRequest true "Object key and expiry"
// @Success 200 {object} object "url and expires_at"
// @Failure 400 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 409 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/buckets/{name}/objects/presign [post]
func (h *BucketHandler) PresignObject(c *gin.Context) {
	bucketName := c.Param("name")
	userID, _ := c.Get("user_id")
	userUUID := userID.(uuid.UUID)

	var req presignRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "Invalid request", Message: err.Error()})
		return
	}

	expiry := presignDefaultExpiry
	if req.ExpiresIn > 0 {
		expiry = time.Duration(req.ExpiresIn) * time.Second
	}
	if expiry < time.Minute {
		expiry = time.Minute
	}
	if expiry > presignMaxExpiry {
		expiry = presignMaxExpiry
	}

	// The caller needs read access to the object.
	allowed, err := h.policyService.CheckObjectAccess(userUUID, bucketName, req.Key, services.ActionGetObject)
	if err != nil || !allowed {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Permission denied",
			Message: "You don't have permission to read this object",
		})
		return
	}

	// The object must exist.
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{Error: "Bucket not found"})
		return
	}
	var obj models.Object
	if err := database.DB.Where("bucket_id = ? AND key = ?", bucket.ID, req.Key).First(&obj).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{Error: "Object not found"})
		return
	}

	// Pick one of the caller's active access keys to sign with. Prefer a key
	// that outlives the requested URL (non-expiring first); if only shorter-
	// lived keys exist, the URL's effective lifetime is capped by the key —
	// the SigV4 verifier rejects expired keys at request time.
	var keys []models.AccessKey
	if err := database.DB.Where("user_id = ? AND is_active = ?", userUUID, true).
		Order("expires_at ASC NULLS FIRST").Find(&keys).Error; err != nil || len(keys) == 0 {
		c.JSON(http.StatusConflict, models.ErrorResponse{
			Error:   "No active access key",
			Message: "Presigned URLs are signed with your access key — generate one in Profile first",
		})
		return
	}
	signingKey := keys[0]
	urlExpiresAt := time.Now().Add(expiry)
	capped := false
	if signingKey.ExpiresAt != nil && signingKey.ExpiresAt.Before(urlExpiresAt) {
		urlExpiresAt = *signingKey.ExpiresAt
		expiry = time.Until(urlExpiresAt)
		capped = true
		if expiry <= 0 {
			c.JSON(http.StatusConflict, models.ErrorResponse{
				Error:   "No usable access key",
				Message: "All your access keys expire before the URL would — generate a longer-lived key",
			})
			return
		}
	}

	secret, err := security.DecryptSecretKey(signingKey.SecretKeyEncrypted)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to sign URL",
			Message: "Could not use the access key for signing",
		})
		return
	}

	endpoint := h.s3PublicEndpoint(c)
	client := s3.NewFromConfig(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(signingKey.AccessKey, secret, ""),
	}, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	presigned, err := s3.NewPresignClient(client).PresignGetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(req.Key),
	}, func(po *s3.PresignOptions) { po.Expires = expiry })
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to sign URL",
			Message: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"url":              presigned.URL,
		"expires_at":       urlExpiresAt.UTC().Format(time.RFC3339),
		"capped_by_key":    capped,
		"signing_key_name": signingKey.Name,
	})
}

// s3PublicEndpoint returns the base URL clients should use to reach the S3
// listener: S3_PUBLIC_ENDPOINT when configured, else derived from the console
// request's host with the S3 API port swapped in.
func (h *BucketHandler) s3PublicEndpoint(c *gin.Context) string {
	if ep := h.config.Server.S3PublicEndpoint; ep != "" {
		return strings.TrimSuffix(ep, "/")
	}
	host := c.Request.Host
	if idx := strings.LastIndex(host, ":"); idx > 0 && !strings.HasSuffix(host, "]") {
		host = host[:idx]
	}
	scheme := "https"
	if c.Request.TLS == nil {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s:%s", scheme, host, h.config.Server.S3APIPort)
}
