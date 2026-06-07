package dns

// web.go — serves the embedded single-page DNS console (web/index.html).
// dns-svc owns its own product UI (M5); the dns.dev.4950.store subdomain's
// nginx points / here, NOT at dock-ui. API/health/metrics paths still 404
// as JSON; everything else falls back to index.html.

import (
	"embed"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed web
var webFS embed.FS

func (p *Plugin) registerWeb(eng *gin.Engine) {
	index, err := webFS.ReadFile("web/index.html")
	if err != nil {
		return // no UI bundled — leave routes API-only
	}
	eng.NoRoute(func(c *gin.Context) {
		reqPath := c.Request.URL.Path
		if strings.HasPrefix(reqPath, "/api/") || reqPath == "/healthz" ||
			reqPath == "/metrics" || strings.HasPrefix(reqPath, "/internal/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		// Serve a real embedded asset when present (future css/js), else
		// fall back to the SPA index.
		if reqPath != "/" {
			if b, err := webFS.ReadFile("web" + reqPath); err == nil {
				ct := mime.TypeByExtension(path.Ext(reqPath))
				if ct == "" {
					ct = "application/octet-stream"
				}
				c.Data(http.StatusOK, ct, b)
				return
			}
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", index)
	})
}
