package dns

// records_handlers.go — record write-through. Every mutation hits the
// PROVIDER first; only on success do we touch the local cache + write an
// audit row. On a provider error the cache is left untouched (the remote
// is the source of truth), and the caller gets a 502.
//
//   POST   /api/dns/zones/:id/records
//   PATCH  /api/dns/records/:id
//   DELETE /api/dns/records/:id

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type createRecordReq struct {
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Priority *int   `json:"priority"`
	Proxied  bool   `json:"proxied"`
	View     string `json:"view"`
}

type updateRecordReq struct {
	Type     *string `json:"type"`
	Name     *string `json:"name"`
	Content  *string `json:"content"`
	TTL      *int    `json:"ttl"`
	Priority *int    `json:"priority"`
	Proxied  *bool   `json:"proxied"`
	View     *string `json:"view"`
}

// validateView normalizes the split-horizon view of a record. Empty defaults
// to "any". Only "any"/"public"/"private" are accepted. Pure — unit-tested
// without a DB. The view is local-cache metadata only; it is never sent to a
// provider (Cloudflare/Name.com never see it).
func validateView(s string) (string, bool) {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case "":
		return "any", true
	case "any", "public", "private":
		return v, true
	default:
		return "", false
	}
}

func recordRowToRecord(r RecordRow) Record {
	return Record{
		RemoteID: r.RemoteRecordID,
		Type:     r.Type,
		Name:     r.Name,
		Content:  r.Content,
		TTL:      r.TTL,
		Priority: r.Priority,
		Proxied:  r.Proxied,
	}
}

// applyRecordPatch overlays the provided (non-nil) fields onto the current
// record. Pure — unit-tested without a DB/provider.
func applyRecordPatch(cur Record, req updateRecordReq) Record {
	if req.Type != nil {
		cur.Type = *req.Type
	}
	if req.Name != nil {
		cur.Name = *req.Name
	}
	if req.Content != nil {
		cur.Content = *req.Content
	}
	if req.TTL != nil {
		cur.TTL = *req.TTL
	}
	if req.Priority != nil {
		cur.Priority = req.Priority
	}
	if req.Proxied != nil {
		cur.Proxied = *req.Proxied
	}
	return cur
}

func (p *Plugin) handleRecordCreate(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	userID := c.GetString(ctxKeyUserID)
	zoneID := strings.TrimSpace(c.Param("id"))

	var req createRecordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	req.Type = strings.ToUpper(strings.TrimSpace(req.Type))
	if req.Type == "" || strings.TrimSpace(req.Content) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "type and content are required"})
		return
	}
	if req.TTL <= 0 {
		req.TTL = 300
	}
	view, ok := validateView(req.View)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "view must be any, public, or private"})
		return
	}

	zone, err := p.getZone(ctx, wsID, zoneID)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "zone not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	prov, code, err := p.providerForZone(ctx, wsID, zone)
	if err != nil {
		c.JSON(code, gin.H{"error": err.Error()})
		return
	}
	if req.Proxied && !prov.Capabilities().Proxied {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider " + zone.ProviderID + " does not support proxied records"})
		return
	}
	if view != "any" && prov.Type() != localType {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a non-default view is only supported on local zones"})
		return
	}

	created, err := prov.CreateRecord(ctx, zone.RemoteZoneID, Record{
		Type: req.Type, Name: req.Name, Content: req.Content, TTL: req.TTL, Priority: req.Priority, Proxied: req.Proxied,
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "create at provider: " + err.Error()})
		return
	}

	row := RecordRow{
		ID: newID("dr_"), WorkspaceID: wsID, ZoneID: zoneID,
		RemoteRecordID: created.RemoteID, Type: created.Type, Name: created.Name,
		Content: created.Content, TTL: created.TTL, Priority: created.Priority, Proxied: created.Proxied,
		View: view,
	}
	if err := p.writeRecordTx(ctx, zoneID, func(tx *sql.Tx) error { return p.insertRecord(ctx, tx, row) }); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "cache insert (record created at provider): " + err.Error()})
		return
	}
	p.writeAudit(ctx, wsID, userID, "record.create", row.ID, nil, row)
	c.JSON(http.StatusCreated, row)
}

func (p *Plugin) handleRecordUpdate(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	userID := c.GetString(ctxKeyUserID)
	recordID := strings.TrimSpace(c.Param("id"))

	var req updateRecordReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	cur, err := p.getRecord(ctx, wsID, recordID)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "record not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	zone, err := p.getZone(ctx, wsID, cur.ZoneID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "zone lookup: " + err.Error()})
		return
	}
	prov, code, err := p.providerForZone(ctx, wsID, zone)
	if err != nil {
		c.JSON(code, gin.H{"error": err.Error()})
		return
	}

	merged := applyRecordPatch(recordRowToRecord(cur), req)
	merged.Type = strings.ToUpper(strings.TrimSpace(merged.Type))
	if merged.Proxied && !prov.Capabilities().Proxied {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider does not support proxied records"})
		return
	}

	// view is local-cache metadata (not part of the domain Record sent to the
	// provider), so it is patched onto the row directly rather than via
	// applyRecordPatch.
	view := cur.View
	if req.View != nil {
		v, ok := validateView(*req.View)
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "view must be any, public, or private"})
			return
		}
		if v != "any" && prov.Type() != localType {
			c.JSON(http.StatusBadRequest, gin.H{"error": "a non-default view is only supported on local zones"})
			return
		}
		view = v
	}

	updated, err := prov.UpdateRecord(ctx, zone.RemoteZoneID, merged)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "update at provider: " + err.Error()})
		return
	}

	row := cur
	row.RemoteRecordID = updated.RemoteID
	row.Type = updated.Type
	row.Name = updated.Name
	row.Content = updated.Content
	row.TTL = updated.TTL
	row.Priority = updated.Priority
	row.Proxied = updated.Proxied
	row.View = view
	if err := p.writeRecordTx(ctx, row.ZoneID, func(tx *sql.Tx) error { return p.updateRecordRow(ctx, tx, row) }); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "cache update (record updated at provider): " + err.Error()})
		return
	}
	p.writeAudit(ctx, wsID, userID, "record.update", recordID, cur, row)
	c.JSON(http.StatusOK, row)
}

func (p *Plugin) handleRecordDelete(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	userID := c.GetString(ctxKeyUserID)
	recordID := strings.TrimSpace(c.Param("id"))

	cur, err := p.getRecord(ctx, wsID, recordID)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "record not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	zone, err := p.getZone(ctx, wsID, cur.ZoneID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "zone lookup: " + err.Error()})
		return
	}
	prov, code, err := p.providerForZone(ctx, wsID, zone)
	if err != nil {
		c.JSON(code, gin.H{"error": err.Error()})
		return
	}

	if err := prov.DeleteRecord(ctx, zone.RemoteZoneID, cur.RemoteRecordID); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "delete at provider: " + err.Error()})
		return
	}
	if err := p.writeRecordTx(ctx, cur.ZoneID, func(tx *sql.Tx) error {
		_, derr := p.deleteRecord(ctx, tx, wsID, recordID)
		return derr
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "cache delete (record deleted at provider): " + err.Error()})
		return
	}
	p.writeAudit(ctx, wsID, userID, "record.delete", recordID, cur, nil)
	c.JSON(http.StatusOK, gin.H{"deleted": true})
}

// providerForZone resolves the live Provider backing a zone. Returns an
// HTTP status code alongside the error for the handler to surface.
func (p *Plugin) providerForZone(ctx context.Context, wsID string, zone ZoneRow) (Provider, int, error) {
	account, err := p.getProvider(ctx, wsID, zone.ProviderID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, http.StatusInternalServerError, errors.New("zone references a missing provider")
	}
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	prov, err := p.buildProvider(account)
	if err != nil {
		return nil, http.StatusInternalServerError, errors.New("build provider: " + err.Error())
	}
	return prov, http.StatusOK, nil
}
