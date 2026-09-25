package middleware

import (
	"mime"
	"net"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// ConsoleCSP is the Content-Security-Policy for the console listener's HTML
// (the embedded React UI and the Swagger UI). Both are served entirely from
// this origin: the Vite build emits external module scripts only (no inline
// <script>), SSO logins are top-level redirects (no third-party scripts), and
// the UI calls the API with same-origin XHR. 'unsafe-inline' for styles covers
// inline style attributes (Swagger UI, React style props); blob:/data: images
// and media cover object previews opened from the bucket browser.
const ConsoleCSP = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; " +
	"media-src 'self' blob:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"frame-ancestors 'none'; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'"

// APICSP is applied to every /api/* response except the Swagger UI. API
// responses are data (JSON, or raw object bytes from the download endpoint
// whose Content-Type is uploader-controlled) and must never execute as a
// document: `sandbox` gives any rendered response an opaque origin with
// scripts disabled, so it cannot reach the console's token storage.
const APICSP = "sandbox; default-src 'none'; frame-ancestors 'none'"

// SecurityHeadersConfig tunes ConsoleSecurityHeaders.
type SecurityHeadersConfig struct {
	// HSTS enables Strict-Transport-Security (only sent on TLS requests, or
	// when TLS is terminated upstream, and never for localhost).
	HSTS bool
	// HSTSMaxAge is the max-age in seconds; <= 0 disables HSTS.
	HSTSMaxAge int
}

// ConsoleSecurityHeaders sets browser hardening headers on every response of
// the console listener. Register it FIRST (before any routes) so it also
// covers /metrics, Swagger and the SPA NoRoute handler.
func ConsoleSecurityHeaders(cfg SecurityHeadersConfig) gin.HandlerFunc {
	hsts := ""
	if cfg.HSTS && cfg.HSTSMaxAge > 0 {
		hsts = "max-age=" + strconv.Itoa(cfg.HSTSMaxAge)
	}
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")

		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") && !strings.HasPrefix(p, "/api/docs") {
			h.Set("Content-Security-Policy", APICSP)
		} else {
			h.Set("Content-Security-Policy", ConsoleCSP)
		}

		if hsts != "" && !isLocalHost(c.Request.Host) {
			h.Set("Strict-Transport-Security", hsts)
		}
		c.Next()
	}
}

// isLocalHost reports whether host (optionally with :port) is localhost or an
// IP literal. HSTS is skipped there: it is host-wide (all ports), so pinning
// localhost would force HTTPS onto every other local dev server for a year,
// and browsers ignore it for IP literals anyway.
func isLocalHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	return net.ParseIP(host) != nil
}

// S3ObjectResponseHeaders hardens object bytes served by the S3 listener's
// GetObject. The stored Content-Type is uploader-controlled, so a browser
// following a presigned link to an HTML/SVG/XML/JS object would otherwise
// render it as an active document on the S3 origin. For such types it forces
// `Content-Disposition: attachment` (unless the — signed — request asked for
// a specific disposition via response-content-disposition) and always sends
// `X-Content-Type-Options: nosniff` so browsers never upgrade a passive type.
func S3ObjectResponseHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")

		q := c.Request.URL.Query()
		// Sub-resource reads return S3 XML documents (ListParts, tagging, ...),
		// not object bytes.
		for _, sub := range []string{"uploadId", "tagging", "acl", "attributes", "legal-hold", "retention", "torrent"} {
			if _, ok := q[sub]; ok {
				c.Next()
				return
			}
		}
		if q.Get("response-content-disposition") != "" {
			c.Next()
			return
		}

		w := &dispositionWriter{ResponseWriter: c.Writer}
		c.Writer = w
		c.Next()
		// Header-only responses (HEAD, 304, empty bodies) never hit Write.
		w.apply()
	}
}

// dispositionWriter inspects the final Content-Type just before the headers
// are flushed and marks active content as an attachment.
type dispositionWriter struct {
	gin.ResponseWriter
	done bool
}

func (w *dispositionWriter) apply() {
	if w.done || w.Written() {
		w.done = true
		return
	}
	w.done = true
	status := w.Status()
	if status < 200 || status >= 300 {
		return
	}
	h := w.Header()
	if IsActiveContentType(h.Get("Content-Type")) &&
		!strings.HasPrefix(strings.ToLower(strings.TrimSpace(h.Get("Content-Disposition"))), "attachment") {
		h.Set("Content-Disposition", "attachment")
	}
}

func (w *dispositionWriter) WriteHeaderNow() {
	w.apply()
	w.ResponseWriter.WriteHeaderNow()
}

func (w *dispositionWriter) Write(b []byte) (int, error) {
	w.apply()
	return w.ResponseWriter.Write(b)
}

func (w *dispositionWriter) WriteString(s string) (int, error) {
	w.apply()
	return w.ResponseWriter.WriteString(s)
}

func (w *dispositionWriter) Flush() {
	w.apply()
	w.ResponseWriter.Flush()
}

// IsActiveContentType reports whether a browser may execute script when it
// renders a response of this media type: HTML, SVG, any XML (XHTML/XSLT can
// carry script), and JavaScript.
func IsActiveContentType(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mt = strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	}
	switch mt {
	case "text/html", "application/xhtml+xml", "image/svg+xml",
		"text/xml", "application/xml", "text/xsl", "application/xslt+xml",
		"text/javascript", "application/javascript", "application/x-javascript",
		"application/ecmascript", "text/ecmascript", "text/jscript",
		"multipart/x-mixed-replace":
		return true
	}
	return strings.HasSuffix(mt, "+xml")
}
