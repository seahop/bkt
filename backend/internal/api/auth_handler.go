package api

import (
	"net/http"
	"strings"
	"time"

	"bkt/internal/auth"
	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/metrics"
	"bkt/internal/models"
	"bkt/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type AuthHandler struct {
	config       *config.Config
	loginGuard   *loginGuard
	auditService *services.AuditService
}

func NewAuthHandler(cfg *config.Config) *AuthHandler {
	return &AuthHandler{
		config:       cfg,
		loginGuard:   newLoginGuard(),
		auditService: services.NewAuditService(),
	}
}

// Register creates a new user account
// @Summary Register a new user
// @Description Creates a new user account and returns JWT access and refresh tokens. Registration must be enabled in server configuration.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body models.RegisterRequest true "Registration credentials"
// @Success 201 {object} models.AuthResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 409 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Router /api/auth/register [post]
func (h *AuthHandler) Register(c *gin.Context) {
	// Check if registration is allowed
	if !h.config.Auth.AllowRegistration {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Registration disabled",
			Message: "Public registration is disabled. Please contact an administrator.",
		})
		return
	}

	var req models.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Message: err.Error(),
		})
		return
	}

	// Check if user already exists
	var existingUser models.User
	if err := database.DB.Where("username = ? OR email = ?", req.Username, req.Email).First(&existingUser).Error; err == nil {
		c.JSON(http.StatusConflict, models.ErrorResponse{
			Error:   "User already exists",
			Message: "Username or email is already taken",
		})
		return
	}

	// Hash password
	hashedPassword, err := auth.HashPassword(req.Password, h.config.Auth.BcryptCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to create user",
			Message: "Error hashing password",
		})
		return
	}

	// Create user
	user := models.User{
		Username: req.Username,
		Email:    req.Email,
		Password: hashedPassword,
		IsAdmin:  false, // First user could be admin, but we'll handle that separately
	}

	if err := database.DB.Create(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to create user",
			Message: "An internal error occurred. Please try again.",
		})
		return
	}

	// Generate the access+refresh pair (access carries the refresh JTI so
	// logout can revoke the sibling refresh token).
	accessTokenDuration, _ := time.ParseDuration(h.config.Auth.AccessTokenExpiry)
	refreshTokenDuration, _ := time.ParseDuration(h.config.Auth.RefreshTokenExpiry)
	token, refreshToken, err := auth.GenerateTokenPair(user.ID, user.Username, user.IsAdmin, user.TokenVersion, h.config.Auth.JWTSecret, accessTokenDuration, refreshTokenDuration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate token",
			Message: "An internal error occurred. Please try again.",
		})
		return
	}

	c.JSON(http.StatusCreated, models.AuthResponse{
		Token:        token,
		RefreshToken: refreshToken,
		User:         user,
	})
}

// Login authenticates a user and returns JWT tokens
// @Summary Login with username and password
// @Description Authenticates a user with username and password and returns JWT access and refresh tokens.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body models.LoginRequest true "Login credentials"
// @Success 200 {object} models.AuthResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 401 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Router /api/auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	var req models.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Message: err.Error(),
		})
		return
	}

	guardKey := strings.ToLower(strings.TrimSpace(req.Username))

	// Refuse if this account is temporarily locked out due to repeated failures.
	if h.loginGuard.blocked(guardKey) {
		metrics.AuthFailuresTotal.WithLabelValues("lockout").Inc()
		c.JSON(http.StatusTooManyRequests, models.ErrorResponse{
			Error:   "Too many attempts",
			Message: "Too many failed login attempts. Please try again later.",
		})
		return
	}

	// Find user. To avoid a username-enumeration timing oracle, always run a
	// bcrypt comparison — against a dummy hash when the user doesn't exist —
	// and return an identical generic error for unknown-user and bad-password.
	var user models.User
	found := database.DB.Where("username = ?", req.Username).First(&user).Error == nil

	passwordOK := false
	if found {
		passwordOK = auth.CheckPassword(req.Password, user.Password)
	} else {
		// Constant-work dummy comparison (bcrypt of "dummy", cost 12).
		auth.CheckPassword(req.Password, "$2a$12$C6UzMDM.H6dfI/f/IKcEeO3Jj0j7q0Q3q1kZ0m0m0m0m0m0m0m0mS")
	}

	if !found || !passwordOK {
		metrics.AuthFailuresTotal.WithLabelValues("invalid_credentials").Inc()
		if h.loginGuard.fail(guardKey) {
			metrics.AuthFailuresTotal.WithLabelValues("lockout").Inc()
		}
		if found {
			_ = h.auditService.LogFailure(c, user.ID, user.Username, "auth.login", "user", user.ID.String(), user.Username, "invalid password", nil)
		} else {
			// Unknown usernames matter for forensics too (spraying, typo'd
			// service accounts). Zero-UUID actor; attempted name in metadata.
			_ = h.auditService.LogFailure(c, uuid.Nil, req.Username, "auth.login", "user", "", "", "unknown username", map[string]interface{}{
				"attempted_username": req.Username,
			})
		}
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error:   "Invalid credentials",
			Message: "Username or password is incorrect",
		})
		return
	}

	// Password is correct. Only now reveal a lock (so lock state isn't an
	// enumeration oracle for someone without the password).
	if user.IsLocked {
		metrics.AuthFailuresTotal.WithLabelValues("locked").Inc()
		_ = h.auditService.LogFailure(c, user.ID, user.Username, "auth.login", "user", user.ID.String(), user.Username, "account locked", nil)
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Account locked",
			Message: "This account has been locked. Please contact an administrator.",
		})
		return
	}

	// Successful authentication — clear the failure counter and audit it.
	h.loginGuard.reset(guardKey)
	_ = h.auditService.LogSuccess(c, user.ID, user.Username, "auth.login", "user", user.ID.String(), user.Username, nil)

	// Generate the access+refresh pair (access carries the refresh JTI so
	// logout can revoke the sibling refresh token).
	accessTokenDuration, _ := time.ParseDuration(h.config.Auth.AccessTokenExpiry)
	refreshTokenDuration, _ := time.ParseDuration(h.config.Auth.RefreshTokenExpiry)
	token, refreshToken, err := auth.GenerateTokenPair(user.ID, user.Username, user.IsAdmin, user.TokenVersion, h.config.Auth.JWTSecret, accessTokenDuration, refreshTokenDuration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate token",
			Message: "An internal error occurred. Please try again.",
		})
		return
	}

	c.JSON(http.StatusOK, models.AuthResponse{
		Token:        token,
		RefreshToken: refreshToken,
		User:         user,
	})
}

// RefreshToken generates a new access token using a refresh token
// @Summary Refresh access token
// @Description Generates a new JWT access token using a valid refresh token.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body object true "Refresh token" SchemaExample({"refresh_token":"eyJ..."})
// @Success 200 {object} object "New access token"
// @Failure 400 {object} models.ErrorResponse
// @Failure 401 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Router /api/auth/refresh [post]
func (h *AuthHandler) RefreshToken(c *gin.Context) {
	var req struct {
		RefreshToken string `json:"refresh_token" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Message: err.Error(),
		})
		return
	}

	// Validate refresh token signature and expiry
	claims, err := auth.ValidateToken(req.RefreshToken, h.config.Auth.JWTSecret)
	if err != nil {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error:   "Invalid refresh token",
			Message: "Please log in again",
		})
		return
	}

	// Only genuine refresh tokens may be exchanged here — an access token
	// presented at /refresh is rejected.
	if claims.TokenType != auth.TokenTypeRefresh {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error:   "Invalid refresh token",
			Message: "Please log in again",
		})
		return
	}

	// Get user
	var user models.User
	if err := database.DB.First(&user, "id = ?", claims.UserID).Error; err != nil {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error:   "Invalid session",
			Message: "Please log in again",
		})
		return
	}

	// Reject if the account is locked or the token predates a session
	// invalidation (lock, password change, admin demotion, sign-out-everywhere).
	if user.IsLocked || user.TokenVersion != claims.TokenVersion {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error:   "Authentication failed",
			Message: "Access denied",
		})
		return
	}

	// Rotate the refresh token by atomically CLAIMING it: the unique index on
	// jti makes this insert the race-proof gate — whoever inserts first owns
	// this rotation; any other use of the same token (concurrent or later)
	// hits the conflict and is handled as a revoked token below. This replaces
	// a check-then-insert that let two concurrent refreshes both succeed.
	if claims.ID == "" || claims.ExpiresAt == nil {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error:   "Invalid refresh token",
			Message: "Please log in again",
		})
		return
	}
	res := database.DB.Exec(
		`INSERT INTO revoked_tokens (id, jti, user_id, reason, expires_at, created_at)
		 VALUES (gen_random_uuid(), ?, ?, ?, ?, NOW())
		 ON CONFLICT (jti) DO NOTHING`,
		claims.ID, user.ID, models.RevokedReasonRotated, claims.ExpiresAt.Time)
	if res.Error != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to rotate token",
			Message: "An internal error occurred. Please try again.",
		})
		return
	}
	if res.RowsAffected == 0 {
		// Token was already revoked. Replay of a ROTATED token is a classic
		// theft indicator (OAuth BCP): someone — the legitimate client or an
		// attacker — is holding a superseded token, and we cannot tell which
		// party got the successor. Revoke the whole session family by bumping
		// TokenVersion. Replay of a logout-revoked token is just a stale
		// client retry — reject it without collateral damage.
		var prior models.RevokedToken
		reason := ""
		if database.DB.Where("jti = ?", claims.ID).First(&prior).Error == nil {
			reason = prior.Reason
		}
		if reason == models.RevokedReasonRotated {
			database.DB.Model(&models.User{}).Where("id = ?", user.ID).
				UpdateColumn("token_version", gorm.Expr("token_version + 1"))
			_ = h.auditService.LogFailure(c, user.ID, user.Username, "auth.refresh_reuse", "user",
				user.ID.String(), user.Username,
				"rotated refresh token replayed — all sessions revoked", nil)
		}
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error:   "Invalid refresh token",
			Message: "Please log in again",
		})
		return
	}

	accessTokenDuration, _ := time.ParseDuration(h.config.Auth.AccessTokenExpiry)
	refreshTokenDuration, _ := time.ParseDuration(h.config.Auth.RefreshTokenExpiry)
	newToken, newRefresh, err := auth.GenerateTokenPair(user.ID, user.Username, user.IsAdmin, user.TokenVersion, h.config.Auth.JWTSecret, accessTokenDuration, refreshTokenDuration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate token",
			Message: "An internal error occurred. Please try again.",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"token":         newToken,
		"refresh_token": newRefresh,
	})
}

// Logout revokes the current access token (and optionally the refresh token) by blacklisting their JTIs
// @Summary Logout and revoke tokens
// @Description Revokes the current access token and optionally the refresh token by blacklisting their JTIs.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body object false "Optional refresh token to also revoke" SchemaExample({"refresh_token":"eyJ..."})
// @Success 200 {object} models.SuccessResponse
// @Security BearerAuth
// @Router /api/auth/logout [post]
func (h *AuthHandler) Logout(c *gin.Context) {
	jti, jtiExists := c.Get("token_jti")
	expiresAt, expiresExists := c.Get("token_expires_at")
	userID, _ := c.Get("user_id")
	uid, _ := userID.(uuid.UUID)

	// Revoke the access token
	if jtiExists && expiresExists {
		jtiStr, _ := jti.(string)
		expTime, _ := expiresAt.(time.Time)
		if jtiStr != "" {
			database.DB.Create(&models.RevokedToken{JTI: jtiStr, UserID: uid, Reason: models.RevokedReasonLogout, ExpiresAt: expTime})
		}
	}

	// Revoke the sibling refresh token via the pair JTI embedded in the access
	// token — the frontend deliberately never stores the refresh token, so this
	// is the only way logout can reach it. Expiry is bounded by the configured
	// refresh duration (the row is pruned once it lapses).
	if pairJTI, ok := c.Get("token_pair_jti"); ok {
		if pairStr, _ := pairJTI.(string); pairStr != "" {
			refreshDur, _ := time.ParseDuration(h.config.Auth.RefreshTokenExpiry)
			database.DB.Create(&models.RevokedToken{
				JTI:       pairStr,
				UserID:    uid,
				Reason:    models.RevokedReasonLogout,
				ExpiresAt: time.Now().Add(refreshDur),
			})
		}
	}

	// Optionally revoke the refresh token too (client should send it on logout)
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := c.ShouldBindJSON(&body); err == nil && body.RefreshToken != "" {
		// Parse without expiry enforcement — we want to blacklist even if already expired
		if claims, err := auth.ParseTokenClaims(body.RefreshToken, h.config.Auth.JWTSecret); err == nil && claims.ID != "" {
			database.DB.Create(&models.RevokedToken{
				JTI:       claims.ID,
				UserID:    uid,
				Reason:    models.RevokedReasonLogout,
				ExpiresAt: claims.ExpiresAt.Time,
			})
		}
	}

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: "Successfully logged out",
	})
}
