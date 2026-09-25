package auth

import (
	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/services"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	Email  string   `json:"email"`
	Name   string   `json:"name"`
	Groups []string `json:"groups"`
	// Policies is a pointer so an absent claim (nil: leave policies as an
	// administrator set them) is distinguishable from an empty one (the IdP
	// says "no policies": revoke them all).
	Policies *[]string `json:"policies"`
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

	// Unauthenticated endpoint: bound the body (a JWT is a few KB).
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10)
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

	// Sync policies from SSO claims. With VAULT_POLICIES_AUTHORITATIVE=true
	// Vault is the source of truth: a present claim always replaces them and
	// a present-but-empty claim clears them (offboarding in Vault takes
	// effect). By default a claim only replaces them when it names at least
	// one existing bkt policy, so Vault's usual template (which always sends
	// "policies", often with Vault-only names) doesn't wipe policies an
	// administrator assigned in bkt.
	if claims.Policies != nil {
		if err := h.syncUserPoliciesFromClaims(user, *claims.Policies); err != nil {
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

	// The refresh token always goes into the httpOnly console cookie; the
	// JSON body carries it only for API/script clients (not the console).
	SetRefreshCookie(c, h.config, refreshToken)
	response := struct {
		Token        string       `json:"token"`
		RefreshToken string       `json:"refresh_token,omitempty"`
		User         *models.User `json:"user"`
		IsNewUser    bool         `json:"is_new_user"`
	}{
		Token:     jwtToken,
		User:      user,
		IsNewUser: isNewUser,
	}
	if !IsConsoleClient(c) {
		response.RefreshToken = refreshToken
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

	// An audience is mandatory: without it any token signed by this Vault
	// JWT backend — including ones minted for other services — would log in.
	if h.config.VaultSSO.Audience == "" {
		return nil, fmt.Errorf("VAULT_JWT_AUDIENCE is not configured; refusing to accept tokens without an audience check")
	}
	opts := []jwt.ParserOption{jwt.WithExpirationRequired(), jwt.WithAudience(h.config.VaultSSO.Audience)}
	if h.config.VaultSSO.Issuer != "" {
		opts = append(opts, jwt.WithIssuer(h.config.VaultSSO.Issuer))
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

	// The email column is unique: refuse clearly rather than failing on the
	// constraint (or shadowing a local account).
	var clash int64
	database.DB.Model(&models.User{}).Where("LOWER(email) = LOWER(?)", email).Count(&clash)
	if clash > 0 {
		return nil, false, fmt.Errorf("an account with email %s already exists; an administrator must link it", email)
	}

	// User doesn't exist - create new user (MinIO approach: no policies by default)
	base := sanitizeUsername(name)
	if base == "" {
		base = generateUsernameFromEmail(email)
	}
	username := uniqueUsername(base)

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
// Authoritative mode (VAULT_POLICIES_AUTHORITATIVE=true) replaces the user's
// policies with those named — an empty list removes them all; the default
// replaces only when at least one name matches an existing bkt policy.
func (h *VaultJWTHandler) syncUserPoliciesFromClaims(user *models.User, policyNames []string) error {
	sync := syncUserPoliciesIfAnyMatch
	if h.config.VaultSSO.PoliciesAuthoritative {
		sync = syncUserPoliciesByName
	}
	if err := sync(user, policyNames); err != nil {
		return fmt.Errorf("failed to sync policies: %w", err)
	}
	return nil
}
