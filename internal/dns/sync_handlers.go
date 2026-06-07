package dns

// sync_handlers.go — POST /api/dns/providers/:id/sync. Pulls zones +
// records from the live provider and refreshes the local cache. All
// network I/O (ListZones/ListRecords) happens BEFORE the DB transaction
// opens, so the write tx stays short. The remote provider is the source
// of truth; a sync makes the cache mirror it exactly.

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type syncResult struct {
	Zones   int `json:"zones"`
	Records int `json:"records"`
}

type zoneWithRecords struct {
	zone    Zone
	records []Record
}

func (p *Plugin) handleProviderSync(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	userID := c.GetString(ctxKeyUserID)
	id := strings.TrimSpace(c.Param("id"))

	account, err := p.getProvider(ctx, wsID, id)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	prov, err := p.buildProvider(account)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "build provider: " + err.Error()})
		return
	}

	// --- network phase (no DB tx held) ---
	zones, err := prov.ListZones(ctx)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "list zones: " + err.Error()})
		return
	}
	collected := make([]zoneWithRecords, 0, len(zones))
	totalRecords := 0
	for _, z := range zones {
		recs, err := prov.ListRecords(ctx, z.RemoteID)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "list records for " + z.Name + ": " + err.Error()})
			return
		}
		collected = append(collected, zoneWithRecords{zone: z, records: recs})
		totalRecords += len(recs)
	}

	// --- write phase (short tx) ---
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback() //nolint:errcheck — no-op after Commit

	for _, zr := range collected {
		zoneID, err := p.upsertZone(ctx, tx, wsID, account.ID, zr.zone.Name, zr.zone.RemoteID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "upsert zone: " + err.Error()})
			return
		}
		if err := p.replaceZoneRecords(ctx, tx, wsID, zoneID, zr.records); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "replace records: " + err.Error()})
			return
		}
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "commit: " + err.Error()})
		return
	}

	res := syncResult{Zones: len(zones), Records: totalRecords}
	p.writeAudit(ctx, wsID, userID, "zone.sync", account.ID, nil, res)
	c.JSON(http.StatusOK, res)
}
