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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// OIDCProviderSettings describes one OpenID Connect provider slot. The same
// handler serves the generic OIDC_* provider and the historical VAULT_OIDC_*
// one; only these settings differ.
type OIDCProviderSettings struct {
	Key         string // value stored in users.sso_provider ("oidc", "vault")
	DisplayName string // audit metadata / error copy
	AuditName   string // metadata "provider" value in the audit log

	IssuerURL    string
	ClientID     string
	ClientSecret string // optional: confidential client. PKCE is always used regardless.
	RedirectURL  string
	Scopes       string

	FrontendCallbackPath string // e.g. "/auth/oidc/callback" — where tokens are handed to the SPA
	CookiePrefix         string // e.g. "oidc_" — state/verifier/nonce cookie names

	UsernameClaim string
	GroupsClaim   string
	AdminGroup    string
	UserGroup     string
	PoliciesClaim string
	// PoliciesAuthoritative makes the IdP the source of truth for policy
	// membership even when the policies claim is absent (treated as "none").
	// Without it, an absent claim leaves policies untouched; a present claim
	// (including an empty one) always replaces them.
	PoliciesAuthoritative bool
	LinkByEmail           bool

	// VaultLegacyURLs enables the Vault UI-URL fallback for providers whose
	// discovery document omits endpoints (older Vault releases).
	VaultLegacyURLs bool
}

// OIDCHandler implements the authorization-code flow with PKCE (S256), state
// and nonce, discovery-driven endpoints, JWKS-verified ID tokens, optional
// UserInfo enrichment, and claims-based provisioning.
type OIDCHandler struct {
	cfg *config.Config
	s   OIDCProviderSettings

	httpClient *http.Client

	discMu sync.Mutex
	disc   *oidcDiscovery
	discAt time.Time
}

// oidcHTTPClient carries a timeout so a hung provider can't pin a goroutine.
var oidcHTTPClient = &http.Client{Timeout: 15 * time.Second}

// oidcDiscoveryTTL bounds how long a discovery document is reused. Providers
// rotate keys far more often than endpoints, and JWKS has its own cache.
const oidcDiscoveryTTL = 10 * time.Minute

// oidcStateTTL is the lifetime (seconds) of the state/verifier/nonce cookies:
// long enough for an MFA prompt at the IdP, short enough to bound replay.
const oidcStateTTL = 600

// oidcDiscovery is the subset of an OIDC provider's discovery document we use.
type oidcDiscovery struct {
	Issuer                            string   `json:"issuer"`
	JWKSURI                           string   `json:"jwks_uri"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	UserInfoEndpoint                  string   `json:"userinfo_endpoint"`
	EndSessionEndpoint                string   `json:"end_session_endpoint"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
}

// oidcTokenResponse is the token endpoint's response.
type oidcTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// oidcIdentity is what provisioning needs, already resolved from ID token and
// UserInfo claims. Subject always comes from the verified ID token.
type oidcIdentity struct {
	Subject       string
	Email         string
	EmailVerified bool
	Username      string
	Groups        []string
	HasGroups     bool // the groups claim was present at all
	Policies      []string
	HasPolicies   bool // the policies claim was present at all
}

// roleDecision is the outcome of group→role resolution.
type roleDecision int

const (
	roleAllow roleDecision = iota
	roleDenyNoGroupsClaim
	roleDenyNotMember
)

// NewOIDCHandler builds the generic provider from OIDC_* configuration.
func NewOIDCHandler(cfg *config.Config) *OIDCHandler {
	o := cfg.OIDC
	if !o.Enabled {
		o.IssuerURL = "" // explicit OIDC_ENABLED=false keeps a configured provider off
	}
	return newOIDCHandler(cfg, OIDCProviderSettings{
		Key:                   "oidc",
		DisplayName:           o.ProviderName,
		AuditName:             "oidc",
		IssuerURL:             o.IssuerURL,
		ClientID:              o.ClientID,
		ClientSecret:          o.ClientSecret,
		RedirectURL:           o.RedirectURL,
		Scopes:                o.Scopes,
		FrontendCallbackPath:  "/auth/oidc/callback",
		CookiePrefix:          "oidc_",
		UsernameClaim:         o.UsernameClaim,
		GroupsClaim:           o.GroupsClaim,
		AdminGroup:            o.AdminGroup,
		UserGroup:             o.UserGroup,
		PoliciesClaim:         o.PoliciesClaim,
		PoliciesAuthoritative: o.PoliciesAuthoritative,
		LinkByEmail:           o.LinkByEmail,
	})
}

func newOIDCHandler(cfg *config.Config, s OIDCProviderSettings) *OIDCHandler {
	if s.GroupsClaim == "" {
		s.GroupsClaim = "groups"
	}
	if s.PoliciesClaim == "" {
		s.PoliciesClaim = "policies"
	}
	if s.DisplayName == "" {
		s.DisplayName = "SSO"
	}
	if s.AuditName == "" {
		s.AuditName = s.Key
	}
	return &OIDCHandler{cfg: cfg, s: s, httpClient: oidcHTTPClient}
}

// Enabled reports whether this provider slot is configured.
func (h *OIDCHandler) Enabled() bool {
	return h.s.IssuerURL != "" && h.s.ClientID != ""
}

// Settings exposes the provider settings (display name etc.) to the API layer.
func (h *OIDCHandler) Settings() OIDCProviderSettings { return h.s }

// ── Discovery ────────────────────────────────────────────────────────────────

// discover fetches (and caches) the provider's discovery document so endpoints
// and the JWKS URI come from provider-advertised values rather than hand-built
// URLs.
func (h *OIDCHandler) discover() (*oidcDiscovery, error) {
	h.discMu.Lock()
	defer h.discMu.Unlock()
	if h.disc != nil && time.Since(h.discAt) < oidcDiscoveryTTL {
		return h.disc, nil
	}
	base := strings.TrimSuffix(h.s.IssuerURL, "/")
	resp, err := h.httpClient.Get(base + "/.well-known/openid-configuration")
	if err != nil {
		if h.disc != nil {
			return h.disc, nil // stale but usable beats failing every login on a blip
		}
		return nil, fmt.Errorf("failed to fetch OIDC discovery: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort close of response body
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OIDC discovery returned status %s", resp.Status)
	}
	var d oidcDiscovery
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("failed to decode OIDC discovery: %w", err)
	}
	if d.JWKSURI == "" {
		return nil, fmt.Errorf("OIDC discovery missing jwks_uri")
	}
	// OIDC Discovery §4.3: the advertised issuer MUST be identical to the
	// configured issuer URL — otherwise a document served for one issuer could
	// vouch for tokens minted by another. It also pins ID-token "iss".
	if !issuerMatches(d.Issuer, h.s.IssuerURL) {
		return nil, fmt.Errorf("OIDC discovery issuer %q does not match the configured issuer URL %q", d.Issuer, h.s.IssuerURL)
	}
	h.disc, h.discAt = &d, time.Now()
	return h.disc, nil
}

// ── Initiate ─────────────────────────────────────────────────────────────────

// Initiate starts the authorization-code flow: it mints a PKCE verifier,
// state and nonce, stores them in HttpOnly cookies, and redirects the browser
// to the provider's authorization endpoint.
func (h *OIDCHandler) Initiate(c *gin.Context) {
	if !h.Enabled() {
		c.JSON(http.StatusNotImplemented, models.ErrorResponse{
			Error:   "OIDC not enabled",
			Message: h.s.DisplayName + " login is not configured on this server",
		})
		return
	}

	codeVerifier, err := generateCodeVerifier()
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to generate PKCE verifier", Message: err.Error()})
		return
	}
	state, err := generateRandomString(32)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to generate state", Message: err.Error()})
		return
	}
	nonce, err := generateRandomString(32)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to generate nonce", Message: err.Error()})
		return
	}

	// SameSite=Lax is required: Strict cookies are not sent on the IdP's
	// cross-site top-level redirect back to our callback.
	secure := requestIsHTTPS(c)
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(h.s.CookiePrefix+"oauth_state", state, oidcStateTTL, "/", "", secure, true)
	c.SetCookie(h.s.CookiePrefix+"pkce_verifier", codeVerifier, oidcStateTTL, "/", "", secure, true)
	c.SetCookie(h.s.CookiePrefix+"oidc_nonce", nonce, oidcStateTTL, "/", "", secure, true)

	authEndpoint := ""
	if disc, derr := h.discover(); derr == nil {
		authEndpoint = disc.AuthorizationEndpoint
	}
	authURL, err := h.buildAuthURL(authEndpoint, state, generateCodeChallenge(codeVerifier), nonce)
	if err != nil {
		h.redirectWithError(c, "discovery_failed", err.Error())
		return
	}
	c.Redirect(http.StatusTemporaryRedirect, authURL)
}

// buildAuthURL constructs the authorization request. PKCE (S256) is sent
// unconditionally: providers that don't enforce it ignore the parameters, and
// those that do (Keycloak enforced-PKCE, Okta, Auth0, Entra ID, Authentik)
// reject requests without them.
func (h *OIDCHandler) buildAuthURL(authEndpoint, state, codeChallenge, nonce string) (string, error) {
	if authEndpoint == "" {
		if !h.s.VaultLegacyURLs {
			return "", fmt.Errorf("provider discovery did not advertise an authorization_endpoint")
		}
		// Vault fallback: convert API URL to UI URL for browser-based auth
		// e.g., https://vault.example.com/v1/identity/oidc/provider/default
		//    -> https://vault.example.com/ui/vault/identity/oidc/provider/default/authorize
		authEndpoint = strings.Replace(h.s.IssuerURL, "/v1/", "/ui/vault/", 1) + "/authorize"
	}
	params := url.Values{}
	params.Set("client_id", h.s.ClientID)
	params.Set("redirect_uri", h.s.RedirectURL)
	params.Set("response_type", "code")
	params.Set("scope", ensureOpenIDScope(h.s.Scopes))
	params.Set("state", state)
	params.Set("nonce", nonce)
	params.Set("code_challenge", codeChallenge)
	params.Set("code_challenge_method", "S256")
	sep := "?"
	if strings.Contains(authEndpoint, "?") {
		sep = "&"
	}
	return authEndpoint + sep + params.Encode(), nil
}

// ── Callback ─────────────────────────────────────────────────────────────────

// Callback completes the flow: it checks state, exchanges the code with the
// PKCE verifier, verifies the ID token (signature, issuer, audience, expiry,
// nonce), enriches from UserInfo, resolves role/policies from claims, and
// provisions or updates the local user before issuing bkt's own token pair.
func (h *OIDCHandler) Callback(c *gin.Context) {
	if !h.Enabled() {
		h.redirectWithError(c, "not_enabled", h.s.DisplayName+" login is not configured")
		return
	}
	if errMsg := c.Query("error"); errMsg != "" {
		h.redirectWithError(c, errMsg, c.Query("error_description"))
		return
	}

	state := c.Query("state")
	cookieState, err := c.Cookie(h.s.CookiePrefix + "oauth_state")
	if err != nil || state == "" || state != cookieState {
		h.redirectWithError(c, "invalid_state", "State mismatch - possible CSRF attack or expired login attempt")
		return
	}
	codeVerifier, err := c.Cookie(h.s.CookiePrefix + "pkce_verifier")
	if err != nil || codeVerifier == "" {
		h.redirectWithError(c, "missing_verifier", "PKCE verifier not found - please start the login again")
		return
	}
	nonce, err := c.Cookie(h.s.CookiePrefix + "oidc_nonce")
	if err != nil || nonce == "" {
		h.redirectWithError(c, "missing_nonce", "Nonce not found - please start the login again")
		return
	}
	h.clearCookies(c)

	code := c.Query("code")
	if code == "" {
		h.redirectWithError(c, "missing_code", "Authorization code not provided")
		return
	}

	identity, err := h.authenticate(c.Request.Context(), code, codeVerifier, nonce)
	if err != nil {
		h.redirectWithError(c, "authentication_failed", err.Error())
		return
	}

	audit := services.NewAuditService()

	isAdmin, decision := resolveRole(identity.Groups, identity.HasGroups, h.s.AdminGroup, h.s.UserGroup)
	switch decision {
	case roleDenyNoGroupsClaim:
		_ = audit.LogFailure(c, uuid.Nil, identity.Username, "auth.login", "user", "", identity.Username, "sso denied: no groups claim", h.auditMeta(identity))
		h.redirectWithError(c, "access_denied_no_groups", fmt.Sprintf("Your %s account did not present a %q claim. Ask an administrator to include group membership in the token.", h.s.DisplayName, h.s.GroupsClaim))
		return
	case roleDenyNotMember:
		_ = audit.LogFailure(c, uuid.Nil, identity.Username, "auth.login", "user", "", identity.Username, "sso denied: not in allowed group", h.auditMeta(identity))
		h.redirectWithError(c, "access_denied_group", fmt.Sprintf("Your %s account is not a member of a group that grants access to bkt.", h.s.DisplayName))
		return
	}

	user, err := h.findOrCreateUser(identity, isAdmin)
	if err != nil {
		_ = audit.LogFailure(c, uuid.Nil, identity.Username, "auth.login", "user", "", identity.Username, err.Error(), h.auditMeta(identity))
		h.redirectWithError(c, "user_error", err.Error())
		return
	}
	if user.IsLocked {
		_ = audit.LogFailure(c, user.ID, user.Username, "auth.login", "user", user.ID.String(), user.Username, "account locked", h.auditMeta(identity))
		h.redirectWithError(c, "account_locked", "Account is locked")
		return
	}

	// Admin membership is the IdP's call whenever an admin group is configured:
	// re-apply it on every login so promotions and demotions take effect at
	// once (AuthMiddleware reads is_admin from the live row).
	if h.s.AdminGroup != "" && user.IsAdmin != isAdmin {
		if err := database.DB.Model(user).Update("is_admin", isAdmin).Error; err == nil {
			user.IsAdmin = isAdmin
		}
	}

	// Policies: when the IdP is the source of truth, replace — including with
	// the empty set, so removing someone from every group in the IdP actually
	// revokes their access at next login (offboarding must not fail open).
	if identity.HasPolicies || h.s.PoliciesAuthoritative {
		if err := syncUserPoliciesByName(user, identity.Policies); err != nil {
			_ = audit.LogFailure(c, user.ID, user.Username, "auth.login", "user", user.ID.String(), user.Username, "policy sync failed: "+err.Error(), h.auditMeta(identity))
			h.redirectWithError(c, "policy_sync_failed", "Could not apply your access policies; please try again or contact an administrator.")
			return
		}
		database.DB.Preload("Policies").First(user, user.ID)
	}

	_ = audit.LogSuccess(c, user.ID, user.Username, "auth.login", "user", user.ID.String(), user.Username, h.auditMeta(identity))

	accessTokenDuration, _ := time.ParseDuration(h.cfg.Auth.AccessTokenExpiry)
	refreshTokenDuration, _ := time.ParseDuration(h.cfg.Auth.RefreshTokenExpiry)
	jwtToken, refreshToken, err := GenerateTokenPair(user.ID, user.Username, user.IsAdmin, user.TokenVersion, h.cfg.Auth.JWTSecret, accessTokenDuration, refreshTokenDuration)
	if err != nil {
		h.redirectWithError(c, "token_generation_failed", err.Error())
		return
	}

	// Tokens travel in the URL fragment so they never reach server logs.
	c.Redirect(http.StatusTemporaryRedirect, h.frontendCallback()+"#token="+url.QueryEscape(jwtToken)+"&refresh_token="+url.QueryEscape(refreshToken))
}

// authenticate performs the network half of the callback — code exchange,
// ID-token verification and UserInfo — and returns the resolved identity. It
// touches no database, which keeps it testable against a fake provider.
func (h *OIDCHandler) authenticate(ctx context.Context, code, codeVerifier, nonce string) (*oidcIdentity, error) {
	tokenResp, err := h.exchangeCode(ctx, code, codeVerifier)
	if err != nil {
		return nil, err
	}
	idClaims, err := h.verifyIDToken(tokenResp.IDToken, nonce)
	if err != nil {
		return nil, err
	}
	// The ID token is authoritative for identity (sub); profile and group
	// claims may live only in UserInfo (Entra ID, Okta with custom scopes),
	// so merge it in when the provider offers one. Failure is non-fatal.
	var userInfo map[string]interface{}
	if tokenResp.AccessToken != "" {
		if ui, uerr := h.fetchUserInfo(ctx, tokenResp.AccessToken); uerr == nil {
			// UserInfo MUST carry "sub" and it MUST equal the ID token's
			// subject; otherwise the response is not used (OIDC Core 5.3.2).
			if sub, _ := ui["sub"].(string); sub != "" && sub == idClaims["sub"] {
				userInfo = ui
			}
		}
	}
	return h.buildIdentity(idClaims, userInfo), nil
}

// exchangeCode redeems the authorization code at the token endpoint with the
// PKCE verifier. When a client secret is configured the client also
// authenticates (client_secret_basic, or client_secret_post if that is the
// only method the provider advertises).
func (h *OIDCHandler) exchangeCode(ctx context.Context, code, codeVerifier string) (*oidcTokenResponse, error) {
	disc, err := h.discover()
	if err != nil {
		return nil, err
	}
	tokenEndpoint := disc.TokenEndpoint
	if tokenEndpoint == "" {
		if !h.s.VaultLegacyURLs {
			return nil, fmt.Errorf("provider discovery did not advertise a token_endpoint")
		}
		tokenEndpoint = strings.TrimSuffix(h.s.IssuerURL, "/") + "/token"
	}

	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("code", code)
	data.Set("client_id", h.s.ClientID)
	data.Set("redirect_uri", h.s.RedirectURL)
	data.Set("code_verifier", codeVerifier)

	useBasic := false
	if h.s.ClientSecret != "" {
		useBasic = clientSecretBasicPreferred(disc.TokenEndpointAuthMethodsSupported)
		if !useBasic {
			data.Set("client_secret", h.s.ClientSecret)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if useBasic {
		// RFC 6749 §2.3.1: credentials are form-urlencoded before Basic auth.
		req.SetBasicAuth(url.QueryEscape(h.s.ClientID), url.QueryEscape(h.s.ClientSecret))
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort close of response body
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tokenResp oidcTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("failed to decode token response: %w", err)
	}
	if tokenResp.IDToken == "" {
		return nil, fmt.Errorf("token response contained no id_token (is the \"openid\" scope granted to this client?)")
	}
	return &tokenResp, nil
}

// clientSecretBasicPreferred picks the token-endpoint auth method. Basic is the
// RFC default and universally supported; only fall back to the form-body
// variant when the provider explicitly lists post but not basic.
func clientSecretBasicPreferred(supported []string) bool {
	if len(supported) == 0 {
		return true
	}
	hasBasic, hasPost := false, false
	for _, m := range supported {
		switch m {
		case "client_secret_basic":
			hasBasic = true
		case "client_secret_post":
			hasPost = true
		}
	}
	return hasBasic || !hasPost
}

// verifyIDToken cryptographically verifies the ID token against the provider's
// JWKS and validates issuer, audience, expiry and nonce. The token drives
// privilege assignment, so it must be verified — not merely parsed — even
// though it arrived over a direct TLS token-exchange call.
func (h *OIDCHandler) verifyIDToken(idToken, expectedNonce string) (jwt.MapClaims, error) {
	disc, err := h.discover()
	if err != nil {
		return nil, err
	}
	if disc.Issuer == "" {
		return nil, fmt.Errorf("OIDC discovery did not advertise an issuer")
	}
	opts := []jwt.ParserOption{jwt.WithExpirationRequired(), jwt.WithLeeway(2 * time.Minute), jwt.WithIssuer(disc.Issuer)}
	if h.s.ClientID != "" {
		opts = append(opts, jwt.WithAudience(h.s.ClientID))
	}
	claims := jwt.MapClaims{}
	if err := verifyJWTWithJWKS(idToken, disc.JWKSURI, claims, opts...); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "unexpected signing method") || strings.Contains(msg, "signing method") {
			msg += " (bkt accepts RS256/384/512 and ES256/384/512 ID tokens; configure the client's ID token signature algorithm accordingly)"
		}
		return nil, fmt.Errorf("ID token verification failed: %s", msg)
	}
	nonce, _ := claims["nonce"].(string)
	if expectedNonce == "" || nonce != expectedNonce {
		return nil, fmt.Errorf("ID token nonce mismatch")
	}
	if sub, _ := claims["sub"].(string); sub == "" {
		return nil, fmt.Errorf("ID token missing subject claim")
	}
	return claims, nil
}

// fetchUserInfo calls the provider's UserInfo endpoint, when advertised.
func (h *OIDCHandler) fetchUserInfo(ctx context.Context, accessToken string) (map[string]interface{}, error) {
	disc, err := h.discover()
	if err != nil || disc.UserInfoEndpoint == "" {
		return nil, fmt.Errorf("no userinfo endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, disc.UserInfoEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort close of response body
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo returned status %s", resp.Status)
	}
	// Some providers return a signed JWT here; we only consume plain JSON.
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "json") {
		return nil, fmt.Errorf("userinfo content-type %q not supported", ct)
	}
	var ui map[string]interface{}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&ui); err != nil {
		return nil, err
	}
	return ui, nil
}

// ── Claims → identity ────────────────────────────────────────────────────────

// buildIdentity merges verified ID-token claims with (optional) UserInfo
// claims. UserInfo wins for profile data, the ID token is the only source of
// the subject.
func (h *OIDCHandler) buildIdentity(id jwt.MapClaims, ui map[string]interface{}) *oidcIdentity {
	get := func(name string) (interface{}, bool) {
		if ui != nil {
			if v, ok := ui[name]; ok && v != nil {
				return v, true
			}
		}
		if v, ok := id[name]; ok && v != nil {
			return v, true
		}
		return nil, false
	}
	str := func(name string) string {
		v, ok := get(name)
		if !ok {
			return ""
		}
		return claimToString(v)
	}

	sub, _ := id["sub"].(string)
	email := strings.TrimSpace(str("email"))
	verified := false
	if v, ok := get("email_verified"); ok {
		switch t := v.(type) {
		case bool:
			verified = t
		case string:
			verified = strings.EqualFold(t, "true")
		}
	}

	identity := &oidcIdentity{Subject: sub, Email: email, EmailVerified: verified}
	if v, ok := get(h.s.GroupsClaim); ok {
		identity.HasGroups = true
		identity.Groups = claimToStringSlice(v)
	}
	if v, ok := get(h.s.PoliciesClaim); ok {
		identity.HasPolicies = true
		identity.Policies = claimToStringSlice(v)
	}
	identity.Username = deriveUsername(h.s.UsernameClaim, str, email, sub)
	return identity
}

// deriveUsername picks the username for a *new* account. Precedence: the
// configured claim → preferred_username → name → email local part → subject.
// Existing accounts are matched by subject, so changing the claim later never
// renames anyone.
func deriveUsername(claim string, str func(string) string, email, sub string) string {
	candidates := []string{}
	if claim != "" {
		candidates = append(candidates, str(claim))
	}
	candidates = append(candidates, str("preferred_username"), str("name"))
	if i := strings.Index(email, "@"); i > 0 {
		candidates = append(candidates, email[:i])
	}
	candidates = append(candidates, sub)
	for _, c := range candidates {
		if s := sanitizeUsername(c); s != "" {
			return s
		}
	}
	return "user"
}

var usernameUnsafe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// sanitizeUsername keeps usernames to a conservative character set so they are
// safe in URLs, S3 principals and log lines. Runs of other characters
// (spaces, unicode punctuation) collapse to a single underscore.
func sanitizeUsername(s string) string {
	s = strings.TrimSpace(s)
	s = usernameUnsafe.ReplaceAllString(s, "_")
	s = strings.Trim(s, "._-")
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

// resolveRole maps group membership to (isAdmin, decision):
//   - no groups configured at all: everyone is allowed as a regular user and
//     admin is never touched by SSO (pre-existing behaviour);
//   - admin group only: members are admins, everyone else a regular user;
//   - user group set: non-admins must be members, otherwise access is denied —
//     distinguishing "no groups claim at all" (a mapper is missing) from
//     "claim present but not a member" so the error can say which.
func resolveRole(groups []string, hasGroupsClaim bool, adminGroup, userGroup string) (bool, roleDecision) {
	if adminGroup == "" && userGroup == "" {
		return false, roleAllow
	}
	member := func(g string) bool {
		if g == "" {
			return false
		}
		for _, x := range groups {
			if x == g {
				return true
			}
		}
		return false
	}
	if member(adminGroup) {
		return true, roleAllow
	}
	if userGroup == "" {
		return false, roleAllow
	}
	if member(userGroup) {
		return false, roleAllow
	}
	if !hasGroupsClaim {
		return false, roleDenyNoGroupsClaim
	}
	return false, roleDenyNotMember
}

// ── Provisioning ─────────────────────────────────────────────────────────────

// findOrCreateUser matches on (provider, subject). Unknown subjects may be
// linked to an existing account of the *same provider* by verified email
// (opt-in; solves IdP migrations where every user gets a new subject) and are
// otherwise created. Local password accounts are never linked automatically.
func (h *OIDCHandler) findOrCreateUser(identity *oidcIdentity, isAdmin bool) (*models.User, error) {
	var user models.User
	err := database.DB.Preload("Policies").Where("sso_provider = ? AND sso_id = ?", h.s.Key, identity.Subject).First(&user).Error
	if err == nil {
		// Keep the IdP-asserted address current: it (not the user-editable
		// email column) is what link-by-email matches on.
		if identity.Email != "" && identity.EmailVerified && user.SSOEmail != identity.Email {
			if database.DB.Model(&user).Update("sso_email", identity.Email).Error == nil {
				user.SSOEmail = identity.Email
			}
		}
		return &user, nil
	}
	if err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("failed to look up user: %w", err)
	}

	if h.s.LinkByEmail && identity.EmailVerified && identity.Email != "" {
		linked, lerr := linkSSOAccountByEmail(h.s.Key, identity.Subject, identity.Email)
		if lerr != nil {
			return nil, lerr
		}
		if linked != nil {
			return linked, nil
		}
	}

	email := identity.Email
	if email == "" {
		email = identity.Subject + "@" + h.s.Key
	}
	// The email column is unique: refuse to silently create a second identity
	// for an address that already belongs to a local (or other-provider) account.
	var clash int64
	database.DB.Model(&models.User{}).Where("LOWER(email) = LOWER(?)", email).Count(&clash)
	if clash > 0 {
		return nil, fmt.Errorf("an account with email %s already exists; an administrator must link it or use a different address", email)
	}

	username := uniqueUsername(identity.Username)
	// sso_email is what link-by-email trusts later, so only record an
	// address the IdP actually verified.
	ssoEmail := ""
	if identity.EmailVerified {
		ssoEmail = identity.Email
	}
	user = models.User{
		ID:          uuid.New(),
		Username:    username,
		Email:       email,
		Password:    "", // SSO users have no local password
		IsAdmin:     isAdmin && h.s.AdminGroup != "",
		SSOProvider: h.s.Key,
		SSOID:       identity.Subject,
		SSOEmail:    ssoEmail,
	}
	if err := database.DB.Create(&user).Error; err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}
	database.DB.Preload("Policies").First(&user, user.ID)
	return &user, nil
}

// linkSSOAccountByEmail links an unknown subject to an existing account of the
// same provider whose *IdP-asserted* address (sso_email) matches. The
// user-editable email column is never consulted (a user could otherwise set
// their email to a victim's and be linked to — or have the victim linked to —
// the wrong account). Exactly one match is required. Re-linking bumps the
// account's TokenVersion (ending existing sessions and STS credentials) and
// deactivates its long-lived access keys, since a different IdP identity now
// controls the account. Returns (nil, nil) when there is no match.
func linkSSOAccountByEmail(provider, subject, email string) (*models.User, error) {
	var matches []models.User
	if err := database.DB.Where("sso_provider = ? AND sso_email <> '' AND LOWER(sso_email) = LOWER(?)", provider, email).
		Limit(2).Find(&matches).Error; err != nil {
		return nil, fmt.Errorf("failed to look up user: %w", err)
	}
	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
	default:
		return nil, fmt.Errorf("more than one account matches %s; an administrator must link it manually", email)
	}
	user := matches[0]
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]interface{}{
			"sso_id":        subject,
			"sso_email":     email,
			"token_version": gorm.Expr("token_version + 1"),
		}).Error; err != nil {
			return err
		}
		return tx.Model(&models.AccessKey{}).Where("user_id = ? AND is_active = ?", user.ID, true).
			Update("is_active", false).Error
	})
	if err != nil {
		return nil, fmt.Errorf("failed to link account: %w", err)
	}
	if err := database.DB.Preload("Policies").First(&user, "id = ?", user.ID).Error; err != nil {
		return nil, fmt.Errorf("failed to reload linked account: %w", err)
	}
	return &user, nil
}

// uniqueUsername appends a numeric suffix when the sanitized base collides with
// any existing account (local or SSO), so an IdP can never shadow a local user,
// or with a username still named as a principal in a bucket policy (so a new
// account never inherits a deleted user's bucket grants).
func uniqueUsername(base string) string {
	base = sanitizeUsername(base)
	if base == "" {
		base = "user"
	}
	taken := func(candidate string) bool {
		var n int64
		database.DB.Model(&models.User{}).Where("LOWER(username) = LOWER(?)", candidate).Count(&n)
		return n > 0 || UsernameReferencedByBucketPolicy(candidate)
	}
	candidate := base
	for i := 1; i < 1000; i++ {
		if !taken(candidate) {
			return candidate
		}
		candidate = fmt.Sprintf("%s%d", base, i)
	}
	return base + "-" + uuid.New().String()[:8]
}

// syncUserPoliciesByName makes the IdP the source of truth for policy
// membership: the user's direct policies are replaced by the bkt policies
// named in policyNames — an empty list (or one with no matching names)
// removes them all. Unknown names are ignored.
func syncUserPoliciesByName(user *models.User, policyNames []string) error {
	policies := []models.Policy{}
	if len(policyNames) > 0 {
		if err := database.DB.Where("name IN ?", policyNames).Find(&policies).Error; err != nil {
			return fmt.Errorf("failed to look up policies: %w", err)
		}
	}
	if len(policies) == 0 {
		return database.DB.Model(user).Association("Policies").Clear()
	}
	return database.DB.Model(user).Association("Policies").Replace(policies)
}

// issuerMatches compares an advertised issuer to the configured one, ignoring
// a trailing slash only.
func issuerMatches(advertised, configured string) bool {
	if advertised == "" || configured == "" {
		return false
	}
	return strings.TrimSuffix(advertised, "/") == strings.TrimSuffix(configured, "/")
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func (h *OIDCHandler) auditMeta(identity *oidcIdentity) map[string]interface{} {
	m := map[string]interface{}{"provider": h.s.AuditName, "subject": identity.Subject}
	if len(identity.Groups) > 0 {
		g := append([]string(nil), identity.Groups...)
		sort.Strings(g)
		m["groups"] = g
	}
	return m
}

func (h *OIDCHandler) frontendCallback() string {
	return strings.TrimSuffix(h.cfg.Server.FrontendURL, "/") + h.s.FrontendCallbackPath
}

// redirectWithError hands an error to the SPA callback page via the fragment.
func (h *OIDCHandler) redirectWithError(c *gin.Context, errCode, errDesc string) {
	c.Redirect(http.StatusTemporaryRedirect, h.frontendCallback()+"#error="+url.QueryEscape(errCode)+"&error_description="+url.QueryEscape(errDesc))
}

func (h *OIDCHandler) clearCookies(c *gin.Context) {
	secure := requestIsHTTPS(c)
	for _, n := range []string{"oauth_state", "pkce_verifier", "oidc_nonce"} {
		c.SetCookie(h.s.CookiePrefix+n, "", -1, "/", "", secure, true)
	}
}

// requestIsHTTPS reports whether the browser reached us over TLS, directly or
// via a terminating proxy, so the Secure cookie flag matches reality (a Secure
// cookie over plain http is silently dropped and the callback then fails).
func requestIsHTTPS(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	return strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
}

func ensureOpenIDScope(scopes string) string {
	for _, s := range strings.Fields(scopes) {
		if s == "openid" {
			return strings.Join(strings.Fields(scopes), " ")
		}
	}
	return strings.TrimSpace("openid " + strings.Join(strings.Fields(scopes), " "))
}

func claimToString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "true"
		}
		return "false"
	}
	return ""
}

// claimToStringSlice accepts the shapes IdPs actually emit for multi-valued
// claims: a JSON array, a single string, or a space/comma separated string.
func claimToStringSlice(v interface{}) []string {
	switch t := v.(type) {
	case []interface{}:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s := strings.TrimSpace(claimToString(x)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		sep := " "
		if strings.Contains(t, ",") {
			sep = ","
		}
		out := []string{}
		for _, s := range strings.Split(t, sep) {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// generateCodeVerifier generates a random PKCE code verifier (43 base64url chars).
func generateCodeVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// generateCodeChallenge creates the S256 code challenge from a verifier.
func generateCodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// generateRandomString generates a random URL-safe string from n bytes.
func generateRandomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
