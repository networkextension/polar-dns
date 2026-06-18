package dns

// zones_store.go — persistence for the zone/record cache. The remote
// provider is the source of truth; these rows are a local mirror refreshed
// by sync. Reads are workspace-scoped; sync writes go through a short
// transaction (network I/O happens before the tx opens — see sync.go).

import (
	"context"
	"database/sql"
	"time"
)

// execer is the subset of *sql.DB / *sql.Tx used by the write-path store
// helpers, so a handler can run a cache write + serial bump in one
// transaction (pass the *sql.Tx) or stand-alone (pass p.DB).
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// ZoneRow is a dns_zone row.
type ZoneRow struct {
	ID           string     `json:"id"`
	WorkspaceID  string     `json:"workspace_id"`
	ProviderID   string     `json:"provider_id"`
	ZoneName     string     `json:"zone_name"`
	RemoteZoneID string     `json:"remote_zone_id"`
	Serial       int64      `json:"serial"`
	LastSyncedAt *time.Time `json:"last_synced_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// RecordRow is a dns_record row.
type RecordRow struct {
	ID             string    `json:"id"`
	WorkspaceID    string    `json:"workspace_id"`
	ZoneID         string    `json:"zone_id"`
	RemoteRecordID string    `json:"remote_record_id"`
	Type           string    `json:"type"`
	Name           string    `json:"name"`
	Content        string    `json:"content"`
	TTL            int       `json:"ttl"`
	Priority       *int      `json:"priority,omitempty"`
	Proxied        bool      `json:"proxied"`
	View           string    `json:"view"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// upsertZone inserts or refreshes a zone (keyed by workspace+provider+name)
// and returns its id (existing or new). Runs inside the sync transaction.
func (p *Plugin) upsertZone(ctx context.Context, tx *sql.Tx, wsID, providerID, zoneName, remoteZoneID string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `
		INSERT INTO dns_zone (id, workspace_id, provider_id, zone_name, remote_zone_id, last_synced_at, created_at)
		VALUES ($1,$2,$3,$4,$5, now(), now())
		ON CONFLICT (workspace_id, provider_id, zone_name)
		DO UPDATE SET remote_zone_id = EXCLUDED.remote_zone_id, last_synced_at = now()
		RETURNING id`,
		newID("dz_"), wsID, providerID, zoneName, remoteZoneID).Scan(&id)
	return id, err
}

// createLocalZone inserts a zone directly (no remote discovery), used for
// 'local' providers where zones are declared rather than synced. The remote
// handle is the zone name itself; serial starts at the column default (1).
// Returns the created row, or sql.ErrNoRows-free conflict as an error.
func (p *Plugin) createLocalZone(ctx context.Context, wsID, providerID, zoneName string) (ZoneRow, error) {
	var z ZoneRow
	err := p.DB.QueryRowContext(ctx, `
		INSERT INTO dns_zone (id, workspace_id, provider_id, zone_name, remote_zone_id, created_at)
		VALUES ($1,$2,$3,$4,$4, now())
		RETURNING id, workspace_id, provider_id, zone_name, remote_zone_id, serial, last_synced_at, created_at`,
		newID("dz_"), wsID, providerID, zoneName).
		Scan(&z.ID, &z.WorkspaceID, &z.ProviderID, &z.ZoneName, &z.RemoteZoneID, &z.Serial, &z.LastSyncedAt, &z.CreatedAt)
	return z, err
}

// bumpZoneSerial increments a zone's monotonic serial. Runs on the same
// handle (tx) as the record write so the version reflects the committed
// change. Bumped for every zone; only 'local' serials are consumed downstream.
func (p *Plugin) bumpZoneSerial(ctx context.Context, ex execer, zoneID string) error {
	_, err := ex.ExecContext(ctx, `UPDATE dns_zone SET serial = serial + 1 WHERE id=$1`, zoneID)
	return err
}

// replaceZoneRecords replaces all cached records for a zone with a fresh
// set (delete-all-then-insert), so the cache exactly mirrors the remote
// after a sync. Runs inside the sync transaction.
func (p *Plugin) replaceZoneRecords(ctx context.Context, tx *sql.Tx, wsID, zoneID string, recs []Record) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM dns_record WHERE zone_id=$1`, zoneID); err != nil {
		return err
	}
	for _, r := range recs {
		var pr sql.NullInt64
		if r.Priority != nil {
			pr = sql.NullInt64{Int64: int64(*r.Priority), Valid: true}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO dns_record
				(id, workspace_id, zone_id, remote_record_id, type, name, content, ttl, priority, proxied, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10, now())`,
			newID("dr_"), wsID, zoneID, r.RemoteID, r.Type, r.Name, r.Content, r.TTL, pr, r.Proxied); err != nil {
			return err
		}
	}
	return nil
}

func (p *Plugin) listZones(ctx context.Context, wsID string) ([]ZoneRow, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT id, workspace_id, provider_id, zone_name, remote_zone_id, serial, last_synced_at, created_at
		FROM dns_zone WHERE workspace_id=$1 ORDER BY zone_name`, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ZoneRow{}
	for rows.Next() {
		var z ZoneRow
		if err := rows.Scan(&z.ID, &z.WorkspaceID, &z.ProviderID, &z.ZoneName, &z.RemoteZoneID, &z.Serial, &z.LastSyncedAt, &z.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, z)
	}
	return out, rows.Err()
}

// listLocalZones returns the workspace's zones backed by a 'local' provider —
// the self-hosted authoritative zones the dns resolver renders. Ordered by
// name for a deterministic export (and a stable ETag).
func (p *Plugin) listLocalZones(ctx context.Context, wsID string) ([]ZoneRow, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT z.id, z.workspace_id, z.provider_id, z.zone_name, z.remote_zone_id, z.serial, z.last_synced_at, z.created_at
		FROM dns_zone z JOIN dns_provider pr ON pr.id = z.provider_id
		WHERE z.workspace_id=$1 AND pr.provider_type=$2 ORDER BY z.zone_name`, wsID, localType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ZoneRow{}
	for rows.Next() {
		var z ZoneRow
		if err := rows.Scan(&z.ID, &z.WorkspaceID, &z.ProviderID, &z.ZoneName, &z.RemoteZoneID, &z.Serial, &z.LastSyncedAt, &z.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, z)
	}
	return out, rows.Err()
}

// getZone loads a full zone row scoped to the workspace. Returns
// sql.ErrNoRows when absent.
func (p *Plugin) getZone(ctx context.Context, wsID, zoneID string) (ZoneRow, error) {
	var z ZoneRow
	err := p.DB.QueryRowContext(ctx, `
		SELECT id, workspace_id, provider_id, zone_name, remote_zone_id, serial, last_synced_at, created_at
		FROM dns_zone WHERE workspace_id=$1 AND id=$2`, wsID, zoneID).
		Scan(&z.ID, &z.WorkspaceID, &z.ProviderID, &z.ZoneName, &z.RemoteZoneID, &z.Serial, &z.LastSyncedAt, &z.CreatedAt)
	return z, err
}

// zoneExists reports whether a zone id belongs to the workspace.
func (p *Plugin) zoneExists(ctx context.Context, wsID, zoneID string) (bool, error) {
	var one int
	err := p.DB.QueryRowContext(ctx,
		`SELECT 1 FROM dns_zone WHERE workspace_id=$1 AND id=$2`, wsID, zoneID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (p *Plugin) listRecordsByZone(ctx context.Context, wsID, zoneID string) ([]RecordRow, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT id, workspace_id, zone_id, remote_record_id, type, name, content, ttl, priority, proxied, view, updated_at
		FROM dns_record WHERE workspace_id=$1 AND zone_id=$2 ORDER BY name, type`, wsID, zoneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecordRow{}
	for rows.Next() {
		var r RecordRow
		var pr sql.NullInt64
		if err := rows.Scan(&r.ID, &r.WorkspaceID, &r.ZoneID, &r.RemoteRecordID, &r.Type, &r.Name, &r.Content, &r.TTL, &pr, &r.Proxied, &r.View, &r.UpdatedAt); err != nil {
			return nil, err
		}
		if pr.Valid {
			v := int(pr.Int64)
			r.Priority = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
