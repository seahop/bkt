// Package web embeds the built React frontend into the Go binary and serves it
// from the console listener, so a single binary ships the API and the UI.
//
// At image-build time the real Vite output (frontend/dist) is copied over the
// dist/ directory here before `go build`; the committed dist/index.html is only
// a placeholder so the package compiles without a frontend build present.
package web

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed all:dist
var distFS embed.FS

// uiFS returns the embedded frontend rooted at the dist directory.
func uiFS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Only happens if the dist directory is missing at build time, which the
		// committed placeholder prevents.
		panic(err)
	}
	return sub
}

// RegisterUI mounts the embedded single-page app as the catch-all (NoRoute)
// handler on the given engine. Real files (hashed JS/CSS, images, fonts) are
// served directly; any other path falls back to index.html so client-side
// routing works — the same behaviour as the old nginx `try_files … /index.html`.
//
// This must only be attached to the console engine, which has no `/:bucket` S3
// routes to collide with. API/metrics/health paths are explicitly 404'd here so
// an unknown /api/* request never returns the HTML shell.
func RegisterUI(r *gin.Engine) {
	files := uiFS()
	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api") || p == "/metrics" ||
			strings.HasPrefix(p, "/health") || strings.HasPrefix(p, "/ready") ||
			strings.HasPrefix(p, "/live") {
			c.Status(http.StatusNotFound)
			return
		}
		serveSPA(c, files)
	})
}

func serveSPA(c *gin.Context, files fs.FS) {
	reqPath := strings.TrimPrefix(path.Clean("/"+c.Request.URL.Path), "/")
	if reqPath == "" {
		reqPath = "index.html"
	}

	data, err := fs.ReadFile(files, reqPath)
	if err != nil {
		// Unknown path → SPA client route: serve the shell (no-cache so a new
		// build is always picked up).
		shell, shellErr := fs.ReadFile(files, "index.html")
		if shellErr != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.Header("Cache-Control", "no-cache")
		c.Data(http.StatusOK, "text/html; charset=utf-8", shell)
		return
	}

	// Hashed build assets are content-addressed and safe to cache forever.
	if strings.HasPrefix(reqPath, "assets/") {
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		c.Header("Cache-Control", "no-cache")
	}
	c.Data(http.StatusOK, contentType(reqPath), data)
}

// contentType maps common web extensions explicitly so we don't depend on the
// container's (often minimal) system MIME database.
func contentType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json", ".map":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".ico":
		return "image/x-icon"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".txt":
		return "text/plain; charset=utf-8"
	default:
		if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
			return ct
		}
		return "application/octet-stream"
	}
}
