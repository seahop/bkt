package api

import (
	"net/http"
	"time"

	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/security"
	"bkt/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// bkt-STS: short-lived S3 credentials for the authenticated console user.
// Not AWS STS API-compatible — a pragmatic REST endpoint that mints an
// expiring access key pair, excluded from the per-user key limit and
// hard-deleted by the cleanup sweep after expiry.

const (
	stsDefaultDuration = time.Hour
	stsMaxDuration     = 12 * time.Hour
)

// IssueTemporaryCredentials handles POST /api/sts/credentials
// {"duration_seconds": N, "read_only": bool}.
// @Summary Issue temporary S3 credentials
// @Description Mints a short-lived S3 access key pair for the caller (default 1h, max 12h). Temporary keys don't count against the per-user key limit and are removed automatically after expiry. The secret is shown only once.
// @Tags sts
// @Accept json
// @Produce json
// @Success 201 {object} object
// @Security BearerAuth
// @Router /api/sts/credentials [post]
func (h *AccessKeyHandler) IssueTemporaryCredentials(c *gin.Context) {
	userID, exists := c.Get("user_id")
	if !exists {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{Error: "Unauthorized"})
		return
	}
	userUUID := userID.(uuid.UUID)

	var req struct {
		DurationSeconds int  `json:"duration_seconds"`
		ReadOnly        bool `json:"read_only"`
	}
	if err := c.ShouldBindJSON(&req); err != nil && err.Error() != "EOF" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "Invalid request body", Message: err.Error()})
		return
	}
	dur := stsDefaultDuration
	if req.DurationSeconds > 0 {
		dur = time.Duration(req.DurationSeconds) * time.Second
	}
	if dur > stsMaxDuration {
		dur = stsMaxDuration
	}
	if dur < time.Minute {
		dur = time.Minute
	}

	accessKey, err := security.GenerateAccessKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to generate credentials"})
		return
	}
	secretKey, err := security.GenerateSecretKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to generate credentials"})
		return
	}
	secretHash, err := bcrypt.GenerateFromPassword([]byte(secretKey), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to secure credentials"})
		return
	}
	secretEnc, err := security.EncryptSecretKey(secretKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to secure credentials"})
		return
	}

	expiresAt := time.Now().Add(dur)
	key := models.AccessKey{
		UserID:             userUUID,
		AccessKey:          accessKey,
		SecretKeyHash:      string(secretHash),
		SecretKeyEncrypted: secretEnc,
		Name:               "sts-temporary",
		Temporary:          true,
		IsActive:           true,
		ReadOnly:           req.ReadOnly,
		ExpiresAt:          &expiresAt,
	}
	if err := database.DB.Create(&key).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to store credentials"})
		return
	}

	uid, uname := actor(c)
	_ = services.NewAuditService().LogSuccess(c, uid, uname, "sts.issue", "access_key", key.ID.String(), key.AccessKey,
		map[string]interface{}{"expires_at": expiresAt.UTC().Format(time.RFC3339), "read_only": req.ReadOnly})

	c.JSON(http.StatusCreated, gin.H{
		"access_key": accessKey,
		"secret_key": secretKey,
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
		"read_only":  req.ReadOnly,
	})
}
