package middleware

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// sensitiveQueryParams are query parameters whose values are credentials or
// one-time secrets and must never reach access logs: presigned-URL signatures
// and credentials (SigV4 and SigV2), STS session tokens, and OAuth/OIDC
// authorization codes / state / tokens on the SSO callbacks. Matched
// case-insensitively.
var sensitiveQueryParams = map[string]bool{
	"x-amz-signature":      true,
	"x-amz-credential":     true,
	"x-amz-security-token": true,
	"signature":            true, // SigV2 presigned URLs
	"awsaccesskeyid":       true, // SigV2 presigned URLs
	"code":                 true,
	"state":                true,
	"token":                true,
	"access_token":         true,
	"refresh_token":        true,
	"id_token":             true,
	"client_secret":        true,
}

// RedactQuery returns rawQuery with the values of sensitive parameters
// replaced by "REDACTED". Parameter order and all other bytes are preserved.
func RedactQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	parts := strings.Split(rawQuery, "&")
	for i, part := range parts {
		key, _, hasValue := strings.Cut(part, "=")
		name, err := url.QueryUnescape(key)
		if err != nil {
			name = key
		}
		if sensitiveQueryParams[strings.ToLower(name)] {
			if hasValue {
				parts[i] = key + "=REDACTED"
			}
		}
	}
	return strings.Join(parts, "&")
}

// AccessLogger is gin's default access log with sensitive query parameters
// redacted. gin.Logger() prints the raw query string, which would write
// presigned-URL signatures (valid bearer credentials until expiry) and SSO
// authorization codes into container logs.
func AccessLogger() gin.HandlerFunc {
	return gin.LoggerWithConfig(gin.LoggerConfig{
		Formatter: func(p gin.LogFormatterParams) string {
			path := p.Request.URL.Path
			if raw := p.Request.URL.RawQuery; raw != "" {
				path += "?" + RedactQuery(raw)
			}
			latency := p.Latency
			if latency > time.Minute {
				latency = latency.Truncate(time.Second)
			}
			return fmt.Sprintf("[GIN] %v | %3d | %13v | %15s | %-7s %#v\n%s",
				p.TimeStamp.Format("2006/01/02 - 15:04:05"),
				p.StatusCode,
				latency,
				p.ClientIP,
				p.Method,
				path,
				p.ErrorMessage,
			)
		},
	})
}
