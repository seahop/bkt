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
	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/models"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type GoogleOAuthHandler struct {
	config *config.Config
}

func NewGoogleOAuthHandler(cfg *config.Config) *GoogleOAuthHandler {
	return &GoogleOAuthHandler{config: cfg}
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
	if !h.config.GoogleSSO.Enabled {
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
	if !h.config.GoogleSSO.Enabled {
		c.JSON(http.StatusNotImplemented, models.ErrorResponse{
			Error:   "Google SSO not enabled",
			Message: "Google SSO is not configured on this server",
		})
		return
	}

	// Verify state token (CSRF protection)
	state := c.Query("state")
	cookieState, err := c.Cookie("oauth_state")
	if err != nil || state == "" || state != cookieState {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid state",
			Message: "State token mismatch - possible CSRF attack",
		})
		return
	}

	// Clear the state cookie
	c.SetCookie("oauth_state", "", -1, "/", "", true, true)

	// Get authorization code
	code := c.Query("code")
	if code == "" {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Missing code",
			Message: "Authorization code not provided",
		})
		return
	}

	// Exchange code for token
	token, err := h.exchangeCodeForToken(code)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to exchange code",
			Message: err.Error(),
		})
		return
	}

	// Get user info from Google
	userInfo, err := h.getUserInfo(token.AccessToken)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to get user info",
			Message: err.Error(),
		})
		return
	}

	// Verify email is verified
	if !userInfo.VerifiedEmail {
		c.JSON(http.StatusUnauthorized, models.ErrorResponse{
			Error:   "Email not verified",
			Message: "Your Google email must be verified to use SSO",
		})
		return
	}

	// Find or create user
	user, isNewUser, err := h.findOrCreateUser(userInfo)
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

	// Return success response with redirect
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
