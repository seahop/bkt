package api

import (
	"net/http"
	"time"
	"bkt/internal/auth"
	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type AuthHandler struct {
	config *config.Config
}

func NewAuthHandler(cfg *config.Config) *AuthHandler {
	return &AuthHandler{config: cfg}
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

	// Generate JWT token
	accessTokenDuration, _ := time.ParseDuration(h.config.Auth.AccessTokenExpiry)
	token, err := auth.GenerateToken(user.ID, user.Username, user.IsAdmin, h.config.Auth.JWTSecret, accessTokenDuration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate token",
			Message: "An internal error occurred. Please try again.",
		})
		return
	}

	// Generate refresh token
	refreshTokenDuration, _ := time.ParseDuration(h.config.Auth.RefreshTokenExpiry)
	refreshToken, err := auth.GenerateToken(user.ID, user.Username, user.IsAdmin, h.config.Auth.JWTSecret, refreshTokenDuration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate refresh token",
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

	// Find user
	var user models.User
	if err := database.DB.Where("username = ?", req.Username).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error:   "Invalid credentials",
			Message: "Username or password is incorrect",
		})
		return
	}

	// Check lock before the expensive bcrypt comparison
	if user.IsLocked {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Account locked",
			Message: "This account has been locked. Please contact an administrator.",
		})
		return
	}

	// Check password
	if !auth.CheckPassword(req.Password, user.Password) {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error:   "Invalid credentials",
			Message: "Username or password is incorrect",
		})
		return
	}

	// Generate JWT token
	accessTokenDuration, _ := time.ParseDuration(h.config.Auth.AccessTokenExpiry)
	token, err := auth.GenerateToken(user.ID, user.Username, user.IsAdmin, h.config.Auth.JWTSecret, accessTokenDuration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate token",
			Message: "An internal error occurred. Please try again.",
		})
		return
	}

	// Generate refresh token
	refreshTokenDuration, _ := time.ParseDuration(h.config.Auth.RefreshTokenExpiry)
	refreshToken, err := auth.GenerateToken(user.ID, user.Username, user.IsAdmin, h.config.Auth.JWTSecret, refreshTokenDuration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate refresh token",
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

	// Check if refresh token has been revoked (e.g. via logout)
	if claims.ID != "" {
		var revoked models.RevokedToken
		if database.DB.Where("jti = ?", claims.ID).First(&revoked).Error == nil {
			c.JSON(http.StatusUnauthorized, models.ErrorResponse{
				Error:   "Invalid refresh token",
				Message: "Please log in again",
			})
			return
		}
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

	// Check if account is locked (use generic message to avoid info disclosure)
	if user.IsLocked {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error:   "Authentication failed",
			Message: "Access denied",
		})
		return
	}

	// Generate new access token
	accessTokenDuration, _ := time.ParseDuration(h.config.Auth.AccessTokenExpiry)
	newToken, err := auth.GenerateToken(user.ID, user.Username, user.IsAdmin, h.config.Auth.JWTSecret, accessTokenDuration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate token",
			Message: "An internal error occurred. Please try again.",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"token": newToken,
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
			database.DB.Create(&models.RevokedToken{JTI: jtiStr, UserID: uid, ExpiresAt: expTime})
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
				ExpiresAt: claims.ExpiresAt.Time,
			})
		}
	}

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: "Successfully logged out",
	})
}
