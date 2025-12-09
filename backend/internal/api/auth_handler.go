package api

import (
	"net/http"
	"bkt/internal/auth"
	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/models"
	"time"

	"github.com/gin-gonic/gin"
)

type AuthHandler struct {
	config *config.Config
}

func NewAuthHandler(cfg *config.Config) *AuthHandler {
	return &AuthHandler{config: cfg}
}

// Register creates a new user account
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
			Message: err.Error(),
		})
		return
	}

	// Generate JWT token
	accessTokenDuration, _ := time.ParseDuration(h.config.Auth.AccessTokenExpiry)
	token, err := auth.GenerateToken(user.ID, user.Username, user.IsAdmin, h.config.Auth.JWTSecret, accessTokenDuration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate token",
			Message: err.Error(),
		})
		return
	}

	// Generate refresh token
	refreshTokenDuration, _ := time.ParseDuration(h.config.Auth.RefreshTokenExpiry)
	refreshToken, err := auth.GenerateToken(user.ID, user.Username, user.IsAdmin, h.config.Auth.JWTSecret, refreshTokenDuration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate refresh token",
			Message: err.Error(),
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

	// Check password
	if !auth.CheckPassword(req.Password, user.Password) {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error:   "Invalid credentials",
			Message: "Username or password is incorrect",
		})
		return
	}

	// Check if account is locked
	if user.IsLocked {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Account locked",
			Message: "This account has been locked. Please contact an administrator.",
		})
		return
	}

	// Generate JWT token
	accessTokenDuration, _ := time.ParseDuration(h.config.Auth.AccessTokenExpiry)
	token, err := auth.GenerateToken(user.ID, user.Username, user.IsAdmin, h.config.Auth.JWTSecret, accessTokenDuration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate token",
			Message: err.Error(),
		})
		return
	}

	// Generate refresh token
	refreshTokenDuration, _ := time.ParseDuration(h.config.Auth.RefreshTokenExpiry)
	refreshToken, err := auth.GenerateToken(user.ID, user.Username, user.IsAdmin, h.config.Auth.JWTSecret, refreshTokenDuration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate refresh token",
			Message: err.Error(),
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

	// Validate refresh token
	claims, err := auth.ValidateToken(req.RefreshToken, h.config.Auth.JWTSecret)
	if err != nil {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error:   "Invalid refresh token",
			Message: err.Error(),
		})
		return
	}

	// Get user
	var user models.User
	if err := database.DB.First(&user, "id = ?", claims.UserID).Error; err != nil {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error:   "User not found",
		})
		return
	}

	// Check if account is locked
	if user.IsLocked {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Account locked",
			Message: "This account has been locked. Please contact an administrator.",
		})
		return
	}

	// Generate new access token
	accessTokenDuration, _ := time.ParseDuration(h.config.Auth.AccessTokenExpiry)
	newToken, err := auth.GenerateToken(user.ID, user.Username, user.IsAdmin, h.config.Auth.JWTSecret, accessTokenDuration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate token",
			Message: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"token": newToken,
	})
}

// Logout invalidates the user's token (in a real implementation, you'd add the token to a blacklist)
func (h *AuthHandler) Logout(c *gin.Context) {
	// In a production system, you would:
	// 1. Add the token to a Redis blacklist
	// 2. Or store token JTI in database with expiry
	// For now, we'll just return success as the client will discard the token

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: "Successfully logged out",
	})
}
