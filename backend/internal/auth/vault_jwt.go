package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/services"
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

	// Sync policies from SSO claims (on every login, SSO is source of truth)
	if len(claims.Policies) > 0 {
		if err := h.syncUserPoliciesFromClaims(user, claims.Policies); err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{
				Error:   "Failed to sync policies",
				Message: err.Error(),
			})
			return
		}
		// Reload user with updated policies
		database.DB.Preload("Policies").First(user, user.ID)
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

	_ = services.NewAuditService().LogSuccess(c, user.ID, user.Username, "auth.login", "user", user.ID.String(), user.Username, map[string]interface{}{"provider": "vault-jwt"})

	// Generate our access+refresh pair (access carries the refresh JTI so
	// logout can revoke the sibling refresh token).
	accessTokenDuration, _ := time.ParseDuration(h.config.Auth.AccessTokenExpiry)
	refreshTokenDuration, _ := time.ParseDuration(h.config.Auth.RefreshTokenExpiry)
	jwtToken, refreshToken, err := GenerateTokenPair(user.ID, user.Username, user.IsAdmin, user.TokenVersion, h.config.Auth.JWTSecret, accessTokenDuration, refreshTokenDuration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate token",
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

// validateVaultJWT cryptographically verifies a Vault-issued JWT. The token's
// signature is checked against Vault's JWKS before ANY claim is trusted — an
// unverified token's claims (subject, email, policies) are entirely
// attacker-controlled, so signature verification is what makes SSO login safe.
func (h *VaultJWTHandler) validateVaultJWT(tokenString string) (*VaultJWTClaims, error) {
	if h.config.VaultSSO.Address == "" || h.config.VaultSSO.JWTPath == "" {
		return nil, fmt.Errorf("vault SSO is not fully configured (address/JWT path missing); refusing to trust token")
	}

	jwksURL := fmt.Sprintf("%s/v1/%s/.well-known/jwks.json", h.config.VaultSSO.Address, h.config.VaultSSO.JWTPath)

	opts := []jwt.ParserOption{jwt.WithExpirationRequired()}
	if h.config.VaultSSO.Audience != "" {
		opts = append(opts, jwt.WithAudience(h.config.VaultSSO.Audience))
	}

	claims := &VaultJWTClaims{}
	if err := verifyJWTWithJWKS(tokenString, jwksURL, claims, opts...); err != nil {
		return nil, fmt.Errorf("token verification failed: %w", err)
	}

	if claims.Subject == "" {
		return nil, fmt.Errorf("token missing subject claim")
	}

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

	resp, err := http.Get(jwksURL) //nolint:gosec // URL built from server-side Vault config, not user input
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort close of response body

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

// syncUserPoliciesFromClaims syncs the user's policies based on SSO JWT claims.
// Policy names in the JWT must match policy names in the database exactly.
// This replaces the user's current policies with those from SSO (SSO is source of truth).
func (h *VaultJWTHandler) syncUserPoliciesFromClaims(user *models.User, policyNames []string) error {
	if len(policyNames) == 0 {
		return nil
	}

	// Look up policies by name
	var policies []models.Policy
	result := database.DB.Where("name IN ?", policyNames).Find(&policies)
	if result.Error != nil {
		return fmt.Errorf("failed to look up policies: %w", result.Error)
	}

	// Replace user's policies with those from SSO
	// This uses GORM's Replace association mode which clears existing and sets new
	if err := database.DB.Model(user).Association("Policies").Replace(policies); err != nil {
		return fmt.Errorf("failed to sync policies: %w", err)
	}

	return nil
}
