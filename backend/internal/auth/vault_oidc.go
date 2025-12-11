package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type VaultOIDCHandler struct {
	config *config.Config
}

func NewVaultOIDCHandler(cfg *config.Config) *VaultOIDCHandler {
	return &VaultOIDCHandler{config: cfg}
}

// VaultTokenResponse represents the token response from Vault OIDC
type VaultTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// VaultIDTokenClaims represents claims in Vault's ID token
type VaultIDTokenClaims struct {
	jwt.RegisteredClaims
	Email    string   `json:"email"`
	Name     string   `json:"name"`
	Groups   []string `json:"groups"`
	Policies []string `json:"policies"`
}

// InitiateVaultLogin starts the OIDC authorization flow with PKCE
func (h *VaultOIDCHandler) InitiateVaultLogin(c *gin.Context) {
	if !h.config.VaultSSO.OIDCEnabled {
		c.JSON(http.StatusNotImplemented, models.ErrorResponse{
			Error:   "Vault OIDC not enabled",
			Message: "Vault OIDC is not configured on this server",
		})
		return
	}

	// Generate PKCE code verifier and challenge
	codeVerifier, err := generateCodeVerifier()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate PKCE verifier",
			Message: err.Error(),
		})
		return
	}
	codeChallenge := generateCodeChallenge(codeVerifier)

	// Generate state for CSRF protection
	state, err := generateRandomString(32)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate state",
			Message: err.Error(),
		})
		return
	}

	// Store state and code_verifier in cookies (HttpOnly, Secure)
	c.SetCookie("vault_oauth_state", state, 600, "/", "", true, true)
	c.SetCookie("vault_pkce_verifier", codeVerifier, 600, "/", "", true, true)

	// Build authorization URL with PKCE
	authURL := h.buildAuthURL(state, codeChallenge)

	c.Redirect(http.StatusTemporaryRedirect, authURL)
}

// HandleVaultCallback handles the OIDC callback with PKCE token exchange
func (h *VaultOIDCHandler) HandleVaultCallback(c *gin.Context) {
	if !h.config.VaultSSO.OIDCEnabled {
		h.redirectWithError(c, "not_enabled", "Vault OIDC is not configured")
		return
	}

	// Check for error from Vault
	if errMsg := c.Query("error"); errMsg != "" {
		errDesc := c.Query("error_description")
		h.redirectWithError(c, errMsg, errDesc)
		return
	}

	// Verify state (CSRF protection)
	state := c.Query("state")
	cookieState, err := c.Cookie("vault_oauth_state")
	if err != nil || state == "" || state != cookieState {
		h.redirectWithError(c, "invalid_state", "State mismatch - possible CSRF attack")
		return
	}

	// Get code verifier from cookie
	codeVerifier, err := c.Cookie("vault_pkce_verifier")
	if err != nil || codeVerifier == "" {
		h.redirectWithError(c, "missing_verifier", "PKCE verifier not found")
		return
	}

	// Clear cookies
	c.SetCookie("vault_oauth_state", "", -1, "/", "", true, true)
	c.SetCookie("vault_pkce_verifier", "", -1, "/", "", true, true)

	// Get authorization code
	code := c.Query("code")
	if code == "" {
		h.redirectWithError(c, "missing_code", "Authorization code not provided")
		return
	}

	// Exchange code for tokens using PKCE
	tokenResp, err := h.exchangeCodeForToken(code, codeVerifier)
	if err != nil {
		h.redirectWithError(c, "token_exchange_failed", err.Error())
		return
	}

	// Parse ID token to get user info
	claims, err := h.parseIDToken(tokenResp.IDToken)
	if err != nil {
		h.redirectWithError(c, "invalid_token", err.Error())
		return
	}

	// Find or create user
	user, err := h.findOrCreateUser(claims)
	if err != nil {
		h.redirectWithError(c, "user_error", err.Error())
		return
	}

	// Check if account is locked
	if user.IsLocked {
		h.redirectWithError(c, "account_locked", "Account is locked")
		return
	}

	// Sync policies from token claims if present
	if len(claims.Policies) > 0 {
		h.syncUserPolicies(user, claims.Policies)
		database.DB.Preload("Policies").First(user, user.ID)
	}

	// Generate our JWT tokens
	accessTokenDuration, _ := time.ParseDuration(h.config.Auth.AccessTokenExpiry)
	jwtToken, err := GenerateToken(user.ID, user.Username, user.IsAdmin, h.config.Auth.JWTSecret, accessTokenDuration)
	if err != nil {
		h.redirectWithError(c, "token_generation_failed", err.Error())
		return
	}

	refreshTokenDuration, _ := time.ParseDuration(h.config.Auth.RefreshTokenExpiry)
	refreshToken, err := GenerateToken(user.ID, user.Username, user.IsAdmin, h.config.Auth.JWTSecret, refreshTokenDuration)
	if err != nil {
		h.redirectWithError(c, "token_generation_failed", err.Error())
		return
	}

	// Redirect to frontend with tokens in URL fragment (keeps them out of server logs)
	frontendURL := strings.TrimSuffix(h.config.Server.FrontendURL, "/")
	redirectURL := frontendURL + "/auth/vault/callback#token=" + url.QueryEscape(jwtToken) +
		"&refresh_token=" + url.QueryEscape(refreshToken)

	c.Redirect(http.StatusTemporaryRedirect, redirectURL)
}

// buildAuthURL constructs the Vault OIDC authorization URL with PKCE
func (h *VaultOIDCHandler) buildAuthURL(state, codeChallenge string) string {
	// Convert API URL to UI URL for browser-based auth
	// e.g., https://vault.example.com/v1/identity/oidc/provider/default
	//    -> https://vault.example.com/ui/vault/identity/oidc/provider/default/authorize
	providerURL := h.config.VaultSSO.ProviderURL
	authEndpoint := strings.Replace(providerURL, "/v1/", "/ui/vault/", 1) + "/authorize"

	params := url.Values{}
	params.Set("client_id", h.config.VaultSSO.ClientID)
	params.Set("redirect_uri", h.config.VaultSSO.RedirectURL)
	params.Set("response_type", "code")
	params.Set("scope", h.config.VaultSSO.Scopes)
	params.Set("state", state)
	// PKCE parameters
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")

	return authEndpoint + "?" + params.Encode()
}

// exchangeCodeForToken exchanges authorization code for tokens using PKCE
func (h *VaultOIDCHandler) exchangeCodeForToken(code, codeVerifier string) (*VaultTokenResponse, error) {
	// Use provider URL + /token
	tokenEndpoint := strings.TrimSuffix(h.config.VaultSSO.ProviderURL, "/") + "/token"

	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("code", code)
	data.Set("client_id", h.config.VaultSSO.ClientID)
	data.Set("redirect_uri", h.config.VaultSSO.RedirectURL)
	// PKCE: send the original code_verifier (no client_secret needed for public clients)
	data.Set("code_verifier", codeVerifier)

	req, err := http.NewRequestWithContext(context.Background(), "POST", tokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, string(body))
	}

	var tokenResp VaultTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("failed to decode token response: %w", err)
	}

	return &tokenResp, nil
}

// parseIDToken extracts claims from the ID token
func (h *VaultOIDCHandler) parseIDToken(idToken string) (*VaultIDTokenClaims, error) {
	// Parse without signature validation (we trust Vault issued this via PKCE flow)
	token, _, err := new(jwt.Parser).ParseUnverified(idToken, &VaultIDTokenClaims{})
	if err != nil {
		return nil, fmt.Errorf("failed to parse ID token: %w", err)
	}

	claims, ok := token.Claims.(*VaultIDTokenClaims)
	if !ok {
		return nil, fmt.Errorf("invalid claims type")
	}

	return claims, nil
}

// findOrCreateUser finds or creates a user from Vault OIDC claims
func (h *VaultOIDCHandler) findOrCreateUser(claims *VaultIDTokenClaims) (*models.User, error) {
	var user models.User

	// Try to find by SSO provider and subject
	result := database.DB.Preload("Policies").Where("sso_provider = ? AND sso_id = ?", "vault", claims.Subject).First(&user)
	if result.Error == nil {
		return &user, nil
	}

	// Create new user
	username := claims.Name
	if username == "" {
		username = claims.Email
	}
	if username == "" {
		username = claims.Subject
	}

	email := claims.Email
	if email == "" {
		email = claims.Subject + "@vault"
	}

	user = models.User{
		ID:          uuid.New(),
		Username:    username,
		Email:       email,
		Password:    "", // No password for SSO users
		IsAdmin:     false,
		SSOProvider: "vault",
		SSOID:       claims.Subject,
		SSOEmail:    email,
	}

	if err := database.DB.Create(&user).Error; err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}

	database.DB.Preload("Policies").First(&user, user.ID)
	return &user, nil
}

// syncUserPolicies syncs policies from token claims
func (h *VaultOIDCHandler) syncUserPolicies(user *models.User, policyNames []string) {
	if len(policyNames) == 0 {
		return
	}

	var policies []models.Policy
	database.DB.Where("name IN ?", policyNames).Find(&policies)

	if len(policies) > 0 {
		database.DB.Model(user).Association("Policies").Replace(policies)
	}
}

// redirectWithError redirects to frontend callback with error in URL fragment
func (h *VaultOIDCHandler) redirectWithError(c *gin.Context, errCode, errDesc string) {
	frontendURL := strings.TrimSuffix(h.config.Server.FrontendURL, "/")
	redirectURL := frontendURL + "/auth/vault/callback#error=" + url.QueryEscape(errCode) +
		"&error_description=" + url.QueryEscape(errDesc)
	c.Redirect(http.StatusTemporaryRedirect, redirectURL)
}

// generateCodeVerifier generates a random PKCE code verifier (43-128 chars)
func generateCodeVerifier() (string, error) {
	// Generate 32 random bytes -> 43 base64url characters
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// generateCodeChallenge creates S256 code challenge from verifier
func generateCodeChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// generateRandomString generates a random URL-safe string
func generateRandomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
