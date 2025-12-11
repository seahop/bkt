package auth

import (
	"context"
	"crypto/rand"
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
	"github.com/google/uuid"
)

type GoogleOAuthHandler struct {
	config           *config.Config
	workspaceService *GoogleWorkspaceService
}

func NewGoogleOAuthHandler(cfg *config.Config) *GoogleOAuthHandler {
	handler := &GoogleOAuthHandler{config: cfg}

	// Initialize workspace service if enabled
	if cfg.GoogleSSO.WorkspaceEnabled {
		handler.workspaceService = NewGoogleWorkspaceService(cfg)
	}

	return handler
}

// GoogleUserInfo represents the user info returned by Google
type GoogleUserInfo struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	VerifiedEmail bool   `json:"verified_email"`
	Name          string `json:"name"`
	GivenName     string `json:"given_name"`
	FamilyName    string `json:"family_name"`
	Picture       string `json:"picture"`
}

// GoogleTokenResponse represents the token response from Google
type GoogleTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	IDToken      string `json:"id_token"`
}

// InitiateGoogleLogin redirects the user to Google's OAuth consent page
func (h *GoogleOAuthHandler) InitiateGoogleLogin(c *gin.Context) {
	if !h.config.GoogleSSO.OIDCEnabled {
		c.JSON(http.StatusNotImplemented, models.ErrorResponse{
			Error:   "Google SSO not enabled",
			Message: "Google SSO is not configured on this server",
		})
		return
	}

	// Generate state token for CSRF protection
	state, err := generateStateToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to generate state",
			Message: err.Error(),
		})
		return
	}

	// Store state in session (in production, use Redis or similar)
	c.SetCookie("oauth_state", state, 600, "/", "", true, true) // 10 minutes

	// Build Google OAuth URL
	authURL := buildGoogleAuthURL(
		h.config.GoogleSSO.ClientID,
		h.config.GoogleSSO.RedirectURL,
		state,
	)

	c.Redirect(http.StatusTemporaryRedirect, authURL)
}

// HandleGoogleCallback handles the callback from Google OAuth
func (h *GoogleOAuthHandler) HandleGoogleCallback(c *gin.Context) {
	if !h.config.GoogleSSO.OIDCEnabled {
		h.redirectWithError(c, "not_enabled", "Google SSO is not configured")
		return
	}

	// Check for error from Google
	if errMsg := c.Query("error"); errMsg != "" {
		errDesc := c.Query("error_description")
		if errDesc == "" {
			errDesc = "Google authentication was cancelled or failed"
		}
		h.redirectWithError(c, errMsg, errDesc)
		return
	}

	// Verify state token (CSRF protection)
	state := c.Query("state")
	cookieState, err := c.Cookie("oauth_state")
	if err != nil || state == "" || state != cookieState {
		h.redirectWithError(c, "invalid_state", "State mismatch - possible CSRF attack")
		return
	}

	// Clear the state cookie
	c.SetCookie("oauth_state", "", -1, "/", "", true, true)

	// Get authorization code
	code := c.Query("code")
	if code == "" {
		h.redirectWithError(c, "missing_code", "Authorization code not provided")
		return
	}

	// Exchange code for token
	token, err := h.exchangeCodeForToken(code)
	if err != nil {
		h.redirectWithError(c, "token_exchange_failed", err.Error())
		return
	}

	// Get user info from Google
	userInfo, err := h.getUserInfo(token.AccessToken)
	if err != nil {
		h.redirectWithError(c, "user_info_failed", err.Error())
		return
	}

	// Verify email is verified
	if !userInfo.VerifiedEmail {
		h.redirectWithError(c, "email_not_verified", "Your Google email must be verified to use SSO")
		return
	}

	// Find or create user
	user, _, err := h.findOrCreateUser(userInfo)
	if err != nil {
		h.redirectWithError(c, "user_error", err.Error())
		return
	}

	// Check if account is locked
	if user.IsLocked {
		h.redirectWithError(c, "account_locked", "This account has been locked")
		return
	}

	// Sync policies from Google Workspace groups (if enabled)
	if h.workspaceService != nil {
		ctx := c.Request.Context()

		// Fetch user's groups from Google Workspace
		groups, err := h.workspaceService.GetUserGroups(ctx, userInfo.Email)
		if err == nil && len(groups) > 0 {
			// Map groups to policy names
			policyNames := h.workspaceService.GetPolicyNamesFromGroups(groups)

			// Sync policies
			if len(policyNames) > 0 {
				if err := h.workspaceService.SyncUserPoliciesFromGroups(user, policyNames); err != nil {
					h.redirectWithError(c, "policy_sync_failed", err.Error())
					return
				}
				// Reload user with updated policies
				database.DB.Preload("Policies").First(user, user.ID)
			}
		}
	}

	// Generate JWT token for our system
	accessTokenDuration, _ := time.ParseDuration(h.config.Auth.AccessTokenExpiry)
	jwtToken, err := GenerateToken(user.ID, user.Username, user.IsAdmin, h.config.Auth.JWTSecret, accessTokenDuration)
	if err != nil {
		h.redirectWithError(c, "token_generation_failed", err.Error())
		return
	}

	// Generate refresh token
	refreshTokenDuration, _ := time.ParseDuration(h.config.Auth.RefreshTokenExpiry)
	refreshToken, err := GenerateToken(user.ID, user.Username, user.IsAdmin, h.config.Auth.JWTSecret, refreshTokenDuration)
	if err != nil {
		h.redirectWithError(c, "token_generation_failed", err.Error())
		return
	}

	// Redirect to frontend with tokens in URL fragment (keeps them out of server logs)
	frontendURL := strings.TrimSuffix(h.config.Server.FrontendURL, "/")
	redirectURL := frontendURL + "/auth/google/callback#token=" + url.QueryEscape(jwtToken) +
		"&refresh_token=" + url.QueryEscape(refreshToken)

	c.Redirect(http.StatusTemporaryRedirect, redirectURL)
}

// redirectWithError redirects to frontend callback with error in URL fragment
func (h *GoogleOAuthHandler) redirectWithError(c *gin.Context, errCode, errDesc string) {
	frontendURL := strings.TrimSuffix(h.config.Server.FrontendURL, "/")
	redirectURL := frontendURL + "/auth/google/callback#error=" + url.QueryEscape(errCode) +
		"&error_description=" + url.QueryEscape(errDesc)
	c.Redirect(http.StatusTemporaryRedirect, redirectURL)
}

// findOrCreateUser finds an existing SSO user or creates a new one
func (h *GoogleOAuthHandler) findOrCreateUser(userInfo *GoogleUserInfo) (*models.User, bool, error) {
	var user models.User

	// First, try to find by SSO provider and ID
	result := database.DB.Preload("Policies").Where("sso_provider = ? AND sso_id = ?", "google", userInfo.ID).First(&user)
	if result.Error == nil {
		// User exists, return it
		return &user, false, nil
	}

	// User doesn't exist - create new user (MinIO approach: no policies by default)
	user = models.User{
		ID:          uuid.New(),
		Username:    generateUsernameFromEmail(userInfo.Email),
		Email:       userInfo.Email,
		Password:    "", // No password for SSO users
		IsAdmin:     false,
		SSOProvider: "google",
		SSOID:       userInfo.ID,
		SSOEmail:    userInfo.Email,
	}

	if err := database.DB.Create(&user).Error; err != nil {
		return nil, false, fmt.Errorf("failed to create user: %w", err)
	}

	// Reload user with policies (will be empty)
	database.DB.Preload("Policies").First(&user, user.ID)

	return &user, true, nil
}

// exchangeCodeForToken exchanges an authorization code for an access token
func (h *GoogleOAuthHandler) exchangeCodeForToken(code string) (*GoogleTokenResponse, error) {
	tokenURL := "https://oauth2.googleapis.com/token"

	data := url.Values{}
	data.Set("code", code)
	data.Set("client_id", h.config.GoogleSSO.ClientID)
	data.Set("client_secret", h.config.GoogleSSO.ClientSecret)
	data.Set("redirect_uri", h.config.GoogleSSO.RedirectURL)
	data.Set("grant_type", "authorization_code")

	resp, err := http.PostForm(tokenURL, data)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token exchange failed: %s - %s", resp.Status, string(body))
	}

	var tokenResp GoogleTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}

	return &tokenResp, nil
}

// getUserInfo fetches user information from Google
func (h *GoogleOAuthHandler) getUserInfo(accessToken string) (*GoogleUserInfo, error) {
	userInfoURL := "https://www.googleapis.com/oauth2/v2/userinfo"

	req, err := http.NewRequestWithContext(context.Background(), "GET", userInfoURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to get user info: %s - %s", resp.Status, string(body))
	}

	var userInfo GoogleUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return nil, err
	}

	return &userInfo, nil
}

// buildGoogleAuthURL builds the Google OAuth authorization URL
func buildGoogleAuthURL(clientID, redirectURL, state string) string {
	baseURL := "https://accounts.google.com/o/oauth2/v2/auth"
	params := url.Values{}
	params.Set("client_id", clientID)
	params.Set("redirect_uri", redirectURL)
	params.Set("response_type", "code")
	params.Set("scope", "openid email profile")
	params.Set("state", state)
	params.Set("access_type", "offline")

	return baseURL + "?" + params.Encode()
}

// generateStateToken generates a random state token for CSRF protection
func generateStateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// generateUsernameFromEmail generates a username from an email address
func generateUsernameFromEmail(email string) string {
	// Split email at @
	parts := split(email, "@")
	if len(parts) > 0 {
		return parts[0]
	}
	return email
}

// split is a helper to split string
func split(s, sep string) []string {
	var result []string
	current := ""
	for _, char := range s {
		if string(char) == sep {
			result = append(result, current)
			current = ""
		} else {
			current += string(char)
		}
	}
	result = append(result, current)
	return result
}
