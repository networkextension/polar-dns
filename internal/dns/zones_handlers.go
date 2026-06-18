package dns

// zones_handlers.go — read endpoints over the local cache:
//   GET /api/dns/zones              — zones in the workspace
//   GET /api/dns/zones/:id/records  — cached records for a zone
// Writes that mutate the cache happen via sync (sync_handlers.go); record
// write-through to the provider lands in M3.

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type createZoneReq struct {
	ProviderID string `json:"provider_id"`
	ZoneName   string `json:"zone_name"`
}

// handleZoneCreate declares a zone under a 'local' provider (POST
// /api/dns/zones). Non-local providers discover their zones via sync, so they
// are rejected here. The remote handle is the zone name itself.
func (p *Plugin) handleZoneCreate(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	userID := c.GetString(ctxKeyUserID)

	var req createZoneReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	req.ProviderID = strings.TrimSpace(req.ProviderID)
	req.ZoneName = strings.TrimSpace(strings.ToLower(req.ZoneName))
	if req.ProviderID == "" || req.ZoneName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider_id and zone_name are required"})
		return
	}

	account, err := p.getProvider(ctx, wsID, req.ProviderID)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if account.ProviderType != localType {
		c.JSON(http.StatusBadRequest, gin.H{"error": "zones for non-local providers are discovered via sync, not created"})
		return
	}

	zone, err := p.createLocalZone(ctx, wsID, req.ProviderID, req.ZoneName)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			c.JSON(http.StatusConflict, gin.H{"error": "zone already exists for this provider"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	p.writeAudit(ctx, wsID, userID, "zone.create", zone.ID, nil, zone)
	c.JSON(http.StatusCreated, zone)
}

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
