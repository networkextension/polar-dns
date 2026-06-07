package dns

// audit_handlers.go — GET /api/dns/audit. Workspace-scoped, newest first,
// ?limit= (default 100, max 500).

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func (p *Plugin) handleAuditList(c *gin.Context) {
	wsID := c.GetString(ctxKeyWorkspaceID)
	limit := 0
	if v := strings.TrimSpace(c.Query("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	rows, err := p.listAudit(c.Request.Context(), wsID, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"audit": rows})
}
