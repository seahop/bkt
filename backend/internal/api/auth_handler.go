package api

import (
	"errors"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

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

// maxAuthBodyBytes caps the JSON body of the unauthenticated auth endpoints
// (login, register, refresh, logout); real payloads are well under 1 KiB.
const maxAuthBodyBytes = 64 << 10

// limitAuthBody bounds how much of the request body the JSON binder will read.
func limitAuthBody(c *gin.Context) {
	if c.Request.Body != nil {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAuthBodyBytes)
	}
}

// maxAuditUsernameLen bounds attacker-supplied usernames written to the audit
// log.
const maxAuditUsernameLen = 255

// truncateForAudit cuts s to at most maxAuditUsernameLen bytes without
// splitting a UTF-8 sequence.
func truncateForAudit(s string) string {
	if len(s) <= maxAuditUsernameLen {
		return s
	}
	cut := maxAuditUsernameLen
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// deliverRefreshToken hands a freshly issued refresh token to the client: it
// always sets the httpOnly console cookie (bkt_refresh) and returns the value
// to put in the JSON body — "" for the web console (X-Bkt-Client: console),
// which must never see the token from script, and the token itself for
// API/script clients, which keep the body-based flow.
func (h *AuthHandler) deliverRefreshToken(c *gin.Context, refreshToken string) string {
	auth.SetRefreshCookie(c, h.config, refreshToken)
	if auth.IsConsoleClient(c) {
		return ""
	}
	return refreshToken
}

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
// @Description Creates a new user account and returns JWT access and refresh tokens. The refresh token is also set in the httpOnly bkt_refresh cookie; with the X-Bkt-Client: console header it is omitted from the body. Registration must be enabled in server configuration.
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

	limitAuthBody(c)
	var req models.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Message: err.Error(),
		})
		return
	}

	if rejectOverlongPassword(c, req.Password) {
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
	// A username still named in a bucket policy (e.g. left over from a
	// deleted account) would inherit that policy's grants — don't hand it out.
	if auth.UsernameReferencedByBucketPolicy(req.Username) {
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
		RefreshToken: h.deliverRefreshToken(c, refreshToken),
		User:         user,
	})
}

// Login authenticates a user and returns JWT tokens
// @Summary Login with username and password
// @Description Authenticates a user with username and password and returns JWT access and refresh tokens. The refresh token is also set in the httpOnly bkt_refresh cookie; with the X-Bkt-Client: console header it is omitted from the body.
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
	limitAuthBody(c)
	var req models.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Message: err.Error(),
		})
		return
	}

	// Lockout is keyed on username + source IP (ClientIP honours the trusted
	// proxy list), so failures from one source can't lock the account for
	// everyone; a higher per-username ceiling throttles distributed guessing
	// but never blocks a correct password (see loginGuard).
	clientIP := c.ClientIP()

	// Refuse outright if this source is hard-locked due to repeated failures.
	decision := h.loginGuard.check(req.Username, clientIP)
	if decision == guardLocked {
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
		if h.loginGuard.fail(req.Username, clientIP) {
			metrics.AuthFailuresTotal.WithLabelValues("lockout").Inc()
		}
		if found {
			_ = h.auditService.LogFailure(c, user.ID, user.Username, "auth.login", "user", user.ID.String(), user.Username, "invalid password", nil)
		} else {
			// Unknown usernames matter for forensics too (spraying, typo'd
			// service accounts). Zero-UUID actor; attempted name in metadata
			// (bounded — the binding caps it too).
			attempted := truncateForAudit(req.Username)
			_ = h.auditService.LogFailure(c, uuid.Nil, attempted, "auth.login", "user", "", "", "unknown username", map[string]interface{}{
				"attempted_username": attempted,
			})
		}
		// While the per-username throttle is active a wrong password gets
		// 429 (a correct one still logs in below).
		if decision == guardThrottled {
			c.JSON(http.StatusTooManyRequests, models.ErrorResponse{
				Error:   "Too many attempts",
				Message: "Too many failed login attempts. Please try again later.",
			})
			return
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

	// Successful authentication — clear this source's failure counter, mark
	// it trusted, and audit it (noting a login through an active throttle).
	h.loginGuard.succeed(req.Username, clientIP)
	var successMeta map[string]interface{}
	if decision == guardThrottled {
		successMeta = map[string]interface{}{"login_throttle_active": true}
	}
	_ = h.auditService.LogSuccess(c, user.ID, user.Username, "auth.login", "user", user.ID.String(), user.Username, successMeta)

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
		RefreshToken: h.deliverRefreshToken(c, refreshToken),
		User:         user,
	})
}

// RefreshToken generates a new access token using a refresh token
// @Summary Refresh access token
// @Description Exchanges a refresh token (from the JSON body, or from the bkt_refresh cookie together with the X-Bkt-Client: console header) for a new access token and a rotated refresh token. The rotated refresh token is set in the cookie and, for non-console clients, returned in the body.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body object false "Refresh token (optional when using the bkt_refresh cookie)" SchemaExample({"refresh_token":"eyJ..."})
// @Success 200 {object} object "New access token"
// @Failure 400 {object} models.ErrorResponse
// @Failure 401 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Router /api/auth/refresh [post]
func (h *AuthHandler) RefreshToken(c *gin.Context) {
	limitAuthBody(c)
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}

	// The body is optional (the console sends none and relies on the cookie);
	// an empty body is fine, a malformed one is not.
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Message: err.Error(),
		})
		return
	}

	// Scripts/API clients present the token in the body (unchanged). The
	// console's lives in the httpOnly bkt_refresh cookie; using it requires the
	// console's custom header, which a cross-site page cannot send without a
	// CORS preflight that the origin allowlist refuses (SameSite=Strict already
	// keeps the cookie off cross-site requests — this is defence in depth).
	refreshToken := req.RefreshToken
	if refreshToken == "" {
		refreshToken = auth.RefreshTokenFromCookie(c)
		if refreshToken == "" {
			c.JSON(http.StatusBadRequest, models.ErrorResponse{
				Error:   "Invalid request",
				Message: "refresh_token is required",
			})
			return
		}
		if !auth.IsConsoleClient(c) {
			c.JSON(http.StatusForbidden, models.ErrorResponse{
				Error:   "Missing client header",
				Message: "Refreshing with the session cookie requires the " + auth.ClientHeader + ": " + auth.ClientConsole + " header",
			})
			return
		}
	}

	// Validate refresh token signature and expiry
	claims, err := auth.ValidateToken(refreshToken, h.config.Auth.JWTSecret)
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
		// Grace window: a replay moments after the rotation is almost always a
		// benign race (two tabs refreshing together, a reload or dropped
		// response while the rotation was in flight, a client retry), not
		// theft. Revoking every session and STS credential for it would log
		// the user out and break their temporary credentials. Within the
		// window, issue a fresh pair instead; outside it, treat as reuse.
		if reason == models.RevokedReasonRotated && time.Since(prior.CreatedAt) < refreshReuseGrace {
			h.issueRefreshedPair(c, &user)
			return
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

	h.issueRefreshedPair(c, &user)
}

// refreshReuseGrace is how long after a refresh token's rotation a replay of
// it is still treated as a benign race rather than token theft.
const refreshReuseGrace = 20 * time.Second

// issueRefreshedPair mints and delivers a new access/refresh pair for user.
func (h *AuthHandler) issueRefreshedPair(c *gin.Context, user *models.User) {
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

	resp := gin.H{"token": newToken}
	if body := h.deliverRefreshToken(c, newRefresh); body != "" {
		resp["refresh_token"] = body
	}
	c.JSON(http.StatusOK, resp)
}

// Logout revokes the current access token and its refresh token(s) by blacklisting their JTIs
// @Summary Logout and revoke tokens
// @Description Revokes the current access token, its sibling refresh token, and any refresh token presented in the body or the bkt_refresh console cookie; always clears the cookie.
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
	// token, so logout reaches it even when the client presents no refresh
	// token (API clients that don't send it; a console whose cookie is gone).
	// Expiry is bounded by the configured refresh duration (the row is pruned
	// once it lapses).
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

	// Also revoke the refresh token the client presents: in the JSON body
	// (API/script clients) and/or the httpOnly bkt_refresh cookie (the web
	// console, which never holds the token in script). After rotations it may
	// no longer be the access token's sibling, so the pair JTI alone isn't
	// enough.
	limitAuthBody(c)
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := c.ShouldBindJSON(&body); err == nil && body.RefreshToken != "" {
		h.revokeRefreshToken(body.RefreshToken, uid)
	}
	if cookieToken := auth.RefreshTokenFromCookie(c); cookieToken != "" && cookieToken != body.RefreshToken {
		h.revokeRefreshToken(cookieToken, uid)
	}
	// Always drop the console cookie, whatever was (or wasn't) revoked.
	auth.ClearRefreshCookie(c, h.config)

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: "Successfully logged out",
	})
}

// revokeRefreshToken blacklists a presented refresh token's JTI. Parsed
// without expiry enforcement: even an expired token is recorded.
func (h *AuthHandler) revokeRefreshToken(token string, uid uuid.UUID) {
	claims, err := auth.ParseTokenClaims(token, h.config.Auth.JWTSecret)
	if err != nil || claims.ID == "" || claims.ExpiresAt == nil {
		return
	}
	database.DB.Create(&models.RevokedToken{
		JTI:       claims.ID,
		UserID:    uid,
		Reason:    models.RevokedReasonLogout,
		ExpiresAt: claims.ExpiresAt.Time,
	})
}
