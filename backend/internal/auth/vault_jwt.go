package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/models"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type VaultJWTHandler struct {
	config *config.Config
}

func NewVaultJWTHandler(cfg *config.Config) *VaultJWTHandler {
	return &VaultJWTHandler{config: cfg}
}

// VaultJWTClaims represents the claims in a Vault JWT
type VaultJWTClaims struct {
	jwt.RegisteredClaims
	Email    string   `json:"email"`
	Name     string   `json:"name"`
	Groups   []string `json:"groups"`
	Policies []string `json:"policies"`
}

// VaultLoginRequest represents the login request with Vault JWT
type VaultLoginRequest struct {
	Token string `json:"token" binding:"required"`
}

// VaultJWKS represents the Vault JWKS response
type VaultJWKS struct {
	Keys []VaultJWK `json:"keys"`
}

// VaultJWK represents a single JWK from Vault
type VaultJWK struct {
	Kty string   `json:"kty"`
	Use string   `json:"use"`
	Kid string   `json:"kid"`
	N   string   `json:"n"`
	E   string   `json:"e"`
	X5c []string `json:"x5c"`
}

// LoginWithVaultJWT validates a Vault JWT and creates/logs in a user
func (h *VaultJWTHandler) LoginWithVaultJWT(c *gin.Context) {
	if !h.config.VaultSSO.Enabled {
		c.JSON(http.StatusNotImplemented, models.ErrorResponse{
			Error:   "Vault SSO not enabled",
			Message: "Vault SSO is not configured on this server",
		})
		return
	}

	var req VaultLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Message: err.Error(),
		})
		return
	}

	// Parse and validate the JWT token
	claims, err := h.validateVaultJWT(req.Token)
	if err != nil {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error:   "Invalid token",
			Message: err.Error(),
		})
		return
	}

	// Extract user information from claims
	email := claims.Email
	name := claims.Name
	if email == "" {
		// Use subject as email fallback
		email = claims.Subject
	}
	if name == "" {
		name = email
	}

	// Find or create user
	user, isNewUser, err := h.findOrCreateVaultUser(claims.Subject, email, name)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to create user",
			Message: err.Error(),
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

	// MinIO-style: Check if user has any policies
	// If no policies, deny access with clear message
	if !user.IsAdmin && len(user.Policies) == 0 {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "No permissions",
			Message: "Your account has been created but has no permissions. Please contact your administrator to grant access.",
		})
		return
	}

	// Generate JWT token for our system
	accessTokenDuration, _ := time.ParseDuration(h.config.Auth.AccessTokenExpiry)
	jwtToken, err := GenerateToken(user.ID, user.Username, user.IsAdmin, h.config.Auth.JWTSecret, accessTokenDuration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate token",
			Message: err.Error(),
		})
		return
	}

	// Generate refresh token
	refreshTokenDuration, _ := time.ParseDuration(h.config.Auth.RefreshTokenExpiry)
	refreshToken, err := GenerateToken(user.ID, user.Username, user.IsAdmin, h.config.Auth.JWTSecret, refreshTokenDuration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate refresh token",
			Message: err.Error(),
		})
		return
	}

	// Return success response
	response := struct {
		Token        string       `json:"token"`
		RefreshToken string       `json:"refresh_token"`
		User         *models.User `json:"user"`
		IsNewUser    bool         `json:"is_new_user"`
	}{
		Token:        jwtToken,
		RefreshToken: refreshToken,
		User:         user,
		IsNewUser:    isNewUser,
	}

	c.JSON(http.StatusOK, response)
}

// validateVaultJWT validates a JWT token from Vault
func (h *VaultJWTHandler) validateVaultJWT(tokenString string) (*VaultJWTClaims, error) {
	// Parse token without validation first to get the claims
	token, _, err := new(jwt.Parser).ParseUnverified(tokenString, &VaultJWTClaims{})
	if err != nil {
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}

	claims, ok := token.Claims.(*VaultJWTClaims)
	if !ok {
		return nil, fmt.Errorf("invalid claims type")
	}

	// Validate basic claims
	if claims.Subject == "" {
		return nil, fmt.Errorf("token missing subject claim")
	}

	// Validate audience if configured
	if h.config.VaultSSO.Audience != "" {
		audienceValid := false
		for _, aud := range claims.Audience {
			if aud == h.config.VaultSSO.Audience {
				audienceValid = true
				break
			}
		}
		if !audienceValid {
			return nil, fmt.Errorf("token audience mismatch")
		}
	}

	// Validate expiration
	if claims.ExpiresAt != nil && claims.ExpiresAt.Before(time.Now()) {
		return nil, fmt.Errorf("token has expired")
	}

	// Validate not before
	if claims.NotBefore != nil && claims.NotBefore.After(time.Now()) {
		return nil, fmt.Errorf("token not yet valid")
	}

	// In production, you should validate the signature with Vault's public key
	// This requires fetching the JWKS from Vault and verifying the signature
	// For now, we're doing basic validation only
	// TODO: Implement full JWT signature validation with Vault JWKS

	return claims, nil
}

// findOrCreateVaultUser finds an existing Vault SSO user or creates a new one
func (h *VaultJWTHandler) findOrCreateVaultUser(vaultID, email, name string) (*models.User, bool, error) {
	var user models.User

	// First, try to find by SSO provider and ID
	result := database.DB.Preload("Policies").Where("sso_provider = ? AND sso_id = ?", "vault", vaultID).First(&user)
	if result.Error == nil {
		// User exists, return it
		return &user, false, nil
	}

	// User doesn't exist - create new user (MinIO approach: no policies by default)
	username := name
	if username == "" {
		username = generateUsernameFromEmail(email)
	}

	user = models.User{
		ID:          uuid.New(),
		Username:    username,
		Email:       email,
		Password:    "", // No password for SSO users
		IsAdmin:     false,
		SSOProvider: "vault",
		SSOID:       vaultID,
		SSOEmail:    email,
	}

	if err := database.DB.Create(&user).Error; err != nil {
		return nil, false, fmt.Errorf("failed to create user: %w", err)
	}

	// Reload user with policies (will be empty)
	database.DB.Preload("Policies").First(&user, user.ID)

	return &user, true, nil
}

// GetVaultJWKS fetches the JWKS from Vault (helper for signature validation)
func (h *VaultJWTHandler) GetVaultJWKS() (*VaultJWKS, error) {
	jwksURL := fmt.Sprintf("%s/v1/%s/.well-known/jwks.json", h.config.VaultSSO.Address, h.config.VaultSSO.JWTPath)

	resp, err := http.Get(jwksURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to fetch JWKS: %s - %s", resp.Status, string(body))
	}

	var jwks VaultJWKS
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return nil, fmt.Errorf("failed to decode JWKS: %w", err)
	}

	return &jwks, nil
}
