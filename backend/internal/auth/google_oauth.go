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
	"bkt/internal/logger"
	"bkt/internal/models"
	"bkt/internal/services"

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

	if cfg.GoogleSSO.OIDCEnabled && len(cfg.GoogleSSO.AllowedDomains) == 0 {
		if cfg.Auth.AllowRegistration {
			logger.Warn("Google SSO: GOOGLE_ALLOWED_DOMAINS is not set and ALLOW_REGISTRATION=true — ANY Google account can sign in and will be auto-provisioned. Set GOOGLE_ALLOWED_DOMAINS to your Workspace domain(s).", nil)
		} else {
			logger.Warn("Google SSO: GOOGLE_ALLOWED_DOMAINS is not set — new Google users will NOT be auto-provisioned (only already-linked accounts can sign in). Set GOOGLE_ALLOWED_DOMAINS to your Workspace domain(s).", nil)
		}
	}

	return handler
}

// GoogleUserInfo represents the user info returned by Google
type GoogleUserInfo struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	VerifiedEmail bool   `json:"verified_email"`
	HostedDomain  string `json:"hd"` // Google Workspace domain; empty for consumer accounts
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
	if !userInfo.VerifiedEmail || userInfo.ID == "" || userInfo.Email == "" {
		h.redirectWithError(c, "email_not_verified", "Your Google email must be verified to use SSO")
		return
	}

	// Restrict to the configured Workspace domain(s).
	if !googleDomainAllowed(h.config.GoogleSSO.AllowedDomains, userInfo) {
		_ = services.NewAuditService().LogFailure(c, uuid.Nil, userInfo.Email, "auth.login", "user", "", userInfo.Email, "google sso denied: domain not allowed", map[string]interface{}{"provider": "google", "hd": userInfo.HostedDomain})
		h.redirectWithError(c, "domain_not_allowed", "This Google account's domain is not allowed to sign in to bkt.")
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

	// Sync policies from Google Workspace groups (if enabled). Workspace owns
	// the MAPPED policies only (those some Workspace group maps to): they are
	// added/removed to match the user's groups — so leaving a group revokes
	// its policy — while policies an administrator assigned by hand are left
	// alone. If the Directory API fails, bkt admins (whose access does not
	// depend on groups) still get in without a sync; everyone else is refused
	// rather than keeping stale (possibly revoked) policies.
	if h.workspaceService != nil {
		ctx := c.Request.Context()

		groups, err := h.workspaceService.GetUserGroups(ctx, userInfo.Email)
		var managed map[string]bool
		if err == nil {
			managed, err = h.workspaceService.GetManagedPolicyNames(ctx)
		}
		switch {
		case err != nil && user.IsAdmin:
			logger.Warn("Google Workspace group lookup failed; admin login allowed without policy sync", map[string]interface{}{"email": userInfo.Email, "error": err.Error()})
			_ = services.NewAuditService().LogFailure(c, user.ID, user.Username, "auth.policy_sync", "user", user.ID.String(), user.Username,
				"google workspace lookup failed; admin login allowed without policy sync", map[string]interface{}{"provider": "google"})
		case err != nil:
			logger.Warn("Google Workspace group lookup failed; refusing login", map[string]interface{}{"email": userInfo.Email, "error": err.Error()})
			_ = services.NewAuditService().LogFailure(c, user.ID, user.Username, "auth.login", "user", user.ID.String(), user.Username,
				"google workspace group lookup failed", map[string]interface{}{"provider": "google"})
			h.redirectWithError(c, "policy_sync_failed", "Could not verify your Google Workspace group membership (the Google Directory API is unavailable or misconfigured); please try again later or contact an administrator.")
			return
		default:
			policyNames := h.workspaceService.GetPolicyNamesFromGroups(groups)
			if err := h.workspaceService.SyncUserPoliciesFromGroups(user, policyNames, managed); err != nil {
				h.redirectWithError(c, "policy_sync_failed", err.Error())
				return
			}
			database.DB.Preload("Policies").First(user, user.ID)
		}
	}

	_ = services.NewAuditService().LogSuccess(c, user.ID, user.Username, "auth.login", "user", user.ID.String(), user.Username, map[string]interface{}{"provider": "google"})

	// Generate our access+refresh pair (access carries the refresh JTI so
	// logout can revoke the sibling refresh token).
	accessTokenDuration, _ := time.ParseDuration(h.config.Auth.AccessTokenExpiry)
	refreshTokenDuration, _ := time.ParseDuration(h.config.Auth.RefreshTokenExpiry)
	jwtToken, refreshToken, err := GenerateTokenPair(user.ID, user.Username, user.IsAdmin, user.TokenVersion, h.config.Auth.JWTSecret, accessTokenDuration, refreshTokenDuration)
	if err != nil {
		h.redirectWithError(c, "token_generation_failed", err.Error())
		return
	}

	// Redirect to the frontend with the access token in the URL fragment
	// (keeps it out of server logs); the refresh token goes only into the
	// httpOnly bkt_refresh cookie.
	frontendURL := strings.TrimSuffix(h.config.Server.FrontendURL, "/")
	completeBrowserSSO(c, h.config, frontendURL+"/auth/google/callback", jwtToken, refreshToken)
}

// redirectWithError redirects to frontend callback with error in URL fragment
func (h *GoogleOAuthHandler) redirectWithError(c *gin.Context, errCode, errDesc string) {
	frontendURL := strings.TrimSuffix(h.config.Server.FrontendURL, "/")
	redirectURL := frontendURL + "/auth/google/callback#error=" + url.QueryEscape(errCode) +
		"&error_description=" + url.QueryEscape(errDesc)
	c.Redirect(http.StatusTemporaryRedirect, redirectURL)
}

// googleDomainAllowed enforces GOOGLE_ALLOWED_DOMAINS: both the Workspace
// hosted-domain ("hd") and the email's domain must be in the list. With no
// list configured every domain passes here (provisioning is then gated by
// ALLOW_REGISTRATION in findOrCreateUser).
func googleDomainAllowed(allowed []string, ui *GoogleUserInfo) bool {
	if len(allowed) == 0 {
		return true
	}
	in := func(d string) bool {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			return false
		}
		for _, a := range allowed {
			if d == a {
				return true
			}
		}
		return false
	}
	at := strings.LastIndex(ui.Email, "@")
	if at < 0 {
		return false
	}
	return in(ui.HostedDomain) && in(ui.Email[at+1:])
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

	// Without a domain allow-list, any Google account on the internet could
	// otherwise self-provision; only do so when registration is open.
	if len(h.config.GoogleSSO.AllowedDomains) == 0 && !h.config.Auth.AllowRegistration {
		return nil, false, fmt.Errorf("no bkt account is linked to this Google account and self-registration is disabled; ask an administrator")
	}

	// The email column is unique: refuse clearly rather than failing on the
	// constraint (or silently shadowing a local account).
	var clash int64
	database.DB.Model(&models.User{}).Where("LOWER(email) = LOWER(?)", userInfo.Email).Count(&clash)
	if clash > 0 {
		return nil, false, fmt.Errorf("an account with email %s already exists; an administrator must link it or use a different address", userInfo.Email)
	}

	// User doesn't exist - create new user (MinIO approach: no policies by default)
	user = models.User{
		ID:          uuid.New(),
		Username:    uniqueUsername(generateUsernameFromEmail(userInfo.Email)),
		Email:       userInfo.Email,
		Password:    "", // No password for SSO users
		IsAdmin:     false,
		SSOProvider: "google",
		SSOID:       userInfo.ID,
		SSOEmail:    userInfo.Email,
	}

	if err := database.DB.Create(&user).Error; err != nil {
		return nil, false, fmt.Errorf("failed to create user account")
	}

	// Reload user with policies (will be empty)
	database.DB.Preload("Policies").First(&user, user.ID)

	return &user, true, nil
}

// exchangeCodeForToken exchanges an authorization code for an access token
func (h *GoogleOAuthHandler) exchangeCodeForToken(code string) (*GoogleTokenResponse, error) {
	tokenURL := "https://oauth2.googleapis.com/token" //nolint:gosec // OAuth token endpoint URL, not a credential

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
	defer resp.Body.Close() //nolint:errcheck // best-effort close of response body

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
	defer resp.Body.Close() //nolint:errcheck // best-effort close of response body

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
