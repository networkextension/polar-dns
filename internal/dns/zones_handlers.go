package dns

// zones_handlers.go — read endpoints over the local cache:
//   GET /api/dns/zones              — zones in the workspace
//   GET /api/dns/zones/:id/records  — cached records for a zone
// Writes that mutate the cache happen via sync (sync_handlers.go); record
// write-through to the provider lands in M3.

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

func (p *Plugin) handleZonesList(c *gin.Context) {
	wsID := c.GetString(ctxKeyWorkspaceID)
	zones, err := p.listZones(c.Request.Context(), wsID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"zones": zones})
}

func (p *Plugin) handleZoneRecords(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	zoneID := strings.TrimSpace(c.Param("id"))

	ok, err := p.zoneExists(ctx, wsID, zoneID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "zone not found"})
		return
	}
	records, err := p.listRecordsByZone(ctx, wsID, zoneID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"records": records})
}
