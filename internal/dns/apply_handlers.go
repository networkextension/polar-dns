package dns

// apply_handlers.go — POST /api/dns/zones/:id/apply. Declarative
// DNS-as-Code: diff a desired record set against the provider's live
// records and apply the plan (write-through, then refresh cache). The
// dnsctl CLI parses zone.yaml locally and posts this JSON; the console
// can post it directly too. dry_run returns the plan without mutating.

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type applyRecordInput struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	// Names: optional bulk fan-out. When set, one record is generated per
	// name (sharing type/content/ttl/priority/proxied) — e.g. names:[a,b,c]
	// on foo.com → a.foo.com, b.foo.com, c.foo.com. Overrides Name.
	Names    []string `json:"names"`
	Priority *int     `json:"priority"`
	Proxied  bool     `json:"proxied"`
}

// expandApplyInputs normalizes inputs to Records, fanning out any entry
// that carries a Names list. Pure — unit-tested.
func expandApplyInputs(inputs []applyRecordInput) []Record {
	out := make([]Record, 0, len(inputs))
	for _, in := range inputs {
		if len(in.Names) > 0 {
			for _, n := range in.Names {
				r := in.toRecord()
				r.Name = normalizeHost(n)
				out = append(out, r)
			}
			continue
		}
		out = append(out, in.toRecord())
	}
	return out
}

func (in applyRecordInput) toRecord() Record {
	ttl := in.TTL
	if ttl <= 0 {
		ttl = 300
	}
	return Record{
		Type:     strings.ToUpper(strings.TrimSpace(in.Type)),
		Name:     normalizeHost(in.Name),
		Content:  strings.TrimSpace(in.Content),
		TTL:      ttl,
		Priority: in.Priority,
		Proxied:  in.Proxied,
	}
}

type applyReq struct {
	DryRun  bool               `json:"dry_run"`
	Prune   bool               `json:"prune"`
	Records []applyRecordInput `json:"records"`
}

func (p *Plugin) handleZoneApply(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	userID := c.GetString(ctxKeyUserID)
	zoneID := strings.TrimSpace(c.Param("id"))

	var req applyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}

	zone, code, perr := p.zoneAndProvider(ctx, wsID, zoneID)
	if perr != nil {
		c.JSON(code, gin.H{"error": perr.Error()})
		return
	}
	prov := zone.prov
	caps := prov.Capabilities()

	desired := expandApplyInputs(req.Records)
	for _, r := range desired {
		if r.Type == "" || r.Content == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "each record needs a type and content"})
			return
		}
		if r.Proxied && !caps.Proxied {
			c.JSON(http.StatusBadRequest, gin.H{"error": "provider does not support proxied records"})
			return
		}
	}

	current, err := prov.ListRecords(ctx, zone.row.RemoteZoneID)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "list current records: " + err.Error()})
		return
	}

	plan := diffRecords(current, desired, req.Prune)
	summary := gin.H{"create": len(plan.Create), "update": len(plan.Update), "delete": len(plan.Delete)}

	if req.DryRun {
		c.JSON(http.StatusOK, gin.H{"dry_run": true, "summary": summary, "plan": plan})
		return
	}

	var errs []string
	created, updated, deleted := 0, 0, 0
	for _, r := range plan.Create {
		if _, e := prov.CreateRecord(ctx, zone.row.RemoteZoneID, r); e != nil {
			errs = append(errs, "create "+r.Type+" "+r.Name+": "+e.Error())
		} else {
			created++
		}
	}
	for _, r := range plan.Update {
		if _, e := prov.UpdateRecord(ctx, zone.row.RemoteZoneID, r); e != nil {
			errs = append(errs, "update "+r.Type+" "+r.Name+": "+e.Error())
		} else {
			updated++
		}
	}
	for _, r := range plan.Delete {
		if e := prov.DeleteRecord(ctx, zone.row.RemoteZoneID, r.RemoteID); e != nil {
			errs = append(errs, "delete "+r.Type+" "+r.Name+": "+e.Error())
		} else {
			deleted++
		}
	}

	// Refresh the cache to mirror the post-apply provider state (best-effort).
	if fresh, e := prov.ListRecords(ctx, zone.row.RemoteZoneID); e == nil {
		if tx, e2 := p.DB.BeginTx(ctx, nil); e2 == nil {
			if e3 := p.replaceZoneRecords(ctx, tx, wsID, zone.row.ID, fresh); e3 == nil {
				_ = tx.Commit()
			} else {
				_ = tx.Rollback()
			}
		}
	} else {
		log.Printf("dns: apply cache refresh skipped (zone %s): %v", zone.row.ID, e)
	}

	applied := gin.H{"created": created, "updated": updated, "deleted": deleted, "prune": req.Prune}
	p.writeAudit(ctx, wsID, userID, "zone.apply", zone.row.ID, nil, applied)

	resp := gin.H{"applied": applied, "summary": summary}
	if len(errs) > 0 {
		resp["errors"] = errs
	}
	c.JSON(http.StatusOK, resp)
}

// zoneAndProvider bundles the getZone + providerForZone lookups,
// returning an HTTP status code on failure.
type zoneWithProvider struct {
	row  ZoneRow
	prov Provider
}

func (p *Plugin) zoneAndProvider(ctx context.Context, wsID, zoneID string) (zoneWithProvider, int, error) {
	zrow, err := p.getZone(ctx, wsID, zoneID)
	if errors.Is(err, sql.ErrNoRows) {
		return zoneWithProvider{}, http.StatusNotFound, errors.New("zone not found")
	}
	if err != nil {
		return zoneWithProvider{}, http.StatusInternalServerError, err
	}
	prov, code, err := p.providerForZone(ctx, wsID, zrow)
	if err != nil {
		return zoneWithProvider{}, code, err
	}
	return zoneWithProvider{row: zrow, prov: prov}, http.StatusOK, nil
}
