package auth

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"bkt/internal/config"

	"github.com/gin-gonic/gin"
)

// Console session delivery.
//
// The web console never sees its refresh token: it is delivered in an
// httpOnly cookie scoped to /api/auth, so script running in the page (XSS)
// cannot read or exfiltrate the long-lived credential. The console marks its
// requests with "X-Bkt-Client: console"; for those, JSON responses omit
// refresh_token. API/script clients (no header) keep receiving refresh_token
// in the body, exactly as before.
const (
	// RefreshCookieName is the httpOnly cookie carrying the console's refresh token.
	RefreshCookieName = "bkt_refresh"
	// RefreshCookiePath scopes the cookie to the auth endpoints (refresh,
	// logout, and the login/SSO responses that set it).
	RefreshCookiePath = "/api/auth"
	// ClientHeader identifies the first-party web console. Being a custom
	// header it forces a CORS preflight on cross-origin requests.
	ClientHeader = "X-Bkt-Client"
	// ClientConsole is the ClientHeader value sent by the web console.
	ClientConsole = "console"
)

// IsConsoleClient reports whether the request comes from the web console.
func IsConsoleClient(c *gin.Context) bool {
	return strings.EqualFold(strings.TrimSpace(c.GetHeader(ClientHeader)), ClientConsole)
}

// refreshCookieSecure: Secure whenever the browser reaches bkt over TLS —
// on the listener itself or at a terminating proxy (TLS_TERMINATED_UPSTREAM).
func refreshCookieSecure(c *gin.Context, cfg *config.Config) bool {
	return cfg.TLS.Enabled || cfg.TLS.TerminatedUpstream || c.Request.TLS != nil
}

// RefreshTokenLifetime is the configured refresh-token lifetime (the cookie's
// Max-Age).
func RefreshTokenLifetime(cfg *config.Config) time.Duration {
	d, _ := time.ParseDuration(cfg.Auth.RefreshTokenExpiry)
	return d
}

// SetRefreshCookie stores refreshToken in the console's httpOnly cookie:
// HttpOnly, SameSite=Strict, Path=/api/auth, host-only (no Domain),
// Max-Age = refresh-token lifetime, Secure when served over TLS.
func SetRefreshCookie(c *gin.Context, cfg *config.Config, refreshToken string) {
	maxAge := int(RefreshTokenLifetime(cfg) / time.Second)
	if maxAge < 0 {
		// Never emit a deleting cookie; 0 = no Max-Age (a session cookie),
		// which is also what an unparseable lifetime yields.
		maxAge = 0
	}
	//nolint:gosec // G124: Secure follows the deployment's TLS (a Secure cookie on a plain-http listener would be dropped); HttpOnly+SameSite=Strict are always set
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     RefreshCookieName,
		Value:    refreshToken,
		Path:     RefreshCookiePath,
		MaxAge:   maxAge,
		Secure:   refreshCookieSecure(c, cfg),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// ClearRefreshCookie expires the console's refresh cookie (Max-Age=0, same
// Path and attributes as when it was set).
func ClearRefreshCookie(c *gin.Context, cfg *config.Config) {
	//nolint:gosec // G124: see SetRefreshCookie; this only expires the cookie
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     RefreshCookieName,
		Value:    "",
		Path:     RefreshCookiePath,
		MaxAge:   -1, // serialised as Max-Age=0
		Secure:   refreshCookieSecure(c, cfg),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// RefreshTokenFromCookie returns the refresh token carried by the console's
// cookie, or "" when absent.
func RefreshTokenFromCookie(c *gin.Context) string {
	v, err := c.Cookie(RefreshCookieName)
	if err != nil {
		return ""
	}
	return v
}

// ssoCallbackURL is where a completed browser SSO flow lands: the SPA callback
// page with ONLY the access token in the fragment (fragments never reach
// server logs). The refresh token travels in the httpOnly cookie instead.
func ssoCallbackURL(frontendCallback, accessToken string) string {
	return frontendCallback + "#token=" + url.QueryEscape(accessToken)
}

// completeBrowserSSO finishes a browser SSO flow: sets the refresh cookie and
// redirects to the SPA callback with the access token in the fragment.
func completeBrowserSSO(c *gin.Context, cfg *config.Config, frontendCallback, accessToken, refreshToken string) {
	SetRefreshCookie(c, cfg, refreshToken)
	c.Redirect(http.StatusTemporaryRedirect, ssoCallbackURL(frontendCallback, accessToken))
}
