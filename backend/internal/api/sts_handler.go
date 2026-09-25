package api

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"bkt/internal/database"
	"bkt/internal/middleware"
	"bkt/internal/models"
	"bkt/internal/security"
	"bkt/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// bkt-STS: short-lived S3 credentials for the authenticated console user.
// Not AWS STS API-compatible — a pragmatic REST endpoint that mints an
// expiring access key pair, excluded from the per-user key limit and
// hard-deleted by the cleanup sweep after expiry. Each credential records the
// user's TokenVersion at issuance, so anything that ends the user's sessions
// (password change, lock, refresh-token reuse detection) also revokes it.

const (
	stsDefaultDuration = time.Hour
	stsMaxDuration     = 12 * time.Hour
	// stsMaxActivePerUser caps concurrently valid temporary credentials per
	// user (they are otherwise unlimited, unlike long-lived keys).
	stsMaxActivePerUser = 10
	// stsIssuePerMinute bounds issuance per user: every credential costs a
	// bcrypt hash, so unthrottled issuance is a cheap CPU-exhaustion lever.
	stsIssuePerMinute = 20
)

var (
	stsLimiterOnce sync.Once
	stsLimiter     *middleware.RateLimiter
)

func stsIssueLimiter() *middleware.RateLimiter {
	stsLimiterOnce.Do(func() { stsLimiter = middleware.NewRateLimiter(stsIssuePerMinute, time.Minute) })
	return stsLimiter
}

var errSTSLimit = errors.New("temporary credential limit reached")

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
	// The response carries a secret: never persist it in the idempotency cache.
	c.Set(middleware.CtxNoIdempotencyStore, true)

	if !stsIssueLimiter().Allow("sts:" + userUUID.String()) {
		c.JSON(http.StatusTooManyRequests, models.ErrorResponse{
			Error:   "Too many requests",
			Message: "Temporary credential issuance is rate limited; try again shortly.",
		})
		return
	}

	// Cheap pre-check before any bcrypt work (re-checked atomically below).
	var active int64
	database.DB.Model(&models.AccessKey{}).
		Where("user_id = ? AND temporary = ? AND is_active = ? AND expires_at > ?", userUUID, true, true, time.Now()).
		Count(&active)
	if active >= stsMaxActivePerUser {
		c.JSON(http.StatusConflict, models.ErrorResponse{
			Error:   "Temporary credential limit reached",
			Message: "You already have the maximum number of active temporary credentials; wait for one to expire.",
		})
		return
	}

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
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		// Lock the user row: serializes issuance per user (cap is exact) and
		// reads the current session generation to bind the credential to.
		var user models.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, "id = ?", userUUID).Error; err != nil {
			return err
		}
		var n int64
		if err := tx.Model(&models.AccessKey{}).
			Where("user_id = ? AND temporary = ? AND is_active = ? AND expires_at > ?", userUUID, true, true, time.Now()).
			Count(&n).Error; err != nil {
			return err
		}
		if n >= stsMaxActivePerUser {
			return errSTSLimit
		}
		tv := user.TokenVersion
		key.IssuerTokenVersion = &tv
		return tx.Create(&key).Error
	})
	if errors.Is(err, errSTSLimit) {
		c.JSON(http.StatusConflict, models.ErrorResponse{
			Error:   "Temporary credential limit reached",
			Message: "You already have the maximum number of active temporary credentials; wait for one to expire.",
		})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to store credentials"})
		return
	}

	uid, uname := actor(c)
	_ = services.NewAuditService().LogSuccess(c, uid, uname, "sts.issue", "access_key", key.ID.String(), key.AccessKey,
		map[string]interface{}{"expires_at": expiresAt.UTC().Format(time.RFC3339), "read_only": req.ReadOnly})

	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, gin.H{
		"access_key": accessKey,
		"secret_key": secretKey,
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
		"read_only":  req.ReadOnly,
	})
}
