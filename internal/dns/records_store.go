package dns

// records_store.go — single-record cache ops used by the write-through
// handlers (records_handlers.go). These touch only the local cache; the
// provider call happens in the handler and must succeed first.

import (
	"context"
	"database/sql"
)

// getRecord loads one cached record scoped to the workspace. Returns
// sql.ErrNoRows when absent.
func (p *Plugin) getRecord(ctx context.Context, wsID, recordID string) (RecordRow, error) {
	var r RecordRow
	var pr sql.NullInt64
	err := p.DB.QueryRowContext(ctx, `
		SELECT id, workspace_id, zone_id, remote_record_id, type, name, content, ttl, priority, proxied, updated_at
		FROM dns_record WHERE workspace_id=$1 AND id=$2`, wsID, recordID).
		Scan(&r.ID, &r.WorkspaceID, &r.ZoneID, &r.RemoteRecordID, &r.Type, &r.Name, &r.Content, &r.TTL, &pr, &r.Proxied, &r.UpdatedAt)
	if err != nil {
		return RecordRow{}, err
	}
	if pr.Valid {
		v := int(pr.Int64)
		r.Priority = &v
	}
	return r, nil
}

// insertRecord writes a freshly-created record into the cache (after the
// provider create succeeded).
func (p *Plugin) insertRecord(ctx context.Context, r RecordRow) error {
	_, err := p.DB.ExecContext(ctx, `
		INSERT INTO dns_record
			(id, workspace_id, zone_id, remote_record_id, type, name, content, ttl, priority, proxied, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10, now())`,
		r.ID, r.WorkspaceID, r.ZoneID, r.RemoteRecordID, r.Type, r.Name, r.Content, r.TTL, priorityArg(r.Priority), r.Proxied)
	return err
}

// updateRecordRow refreshes a cached record (after the provider update
// succeeded).
func (p *Plugin) updateRecordRow(ctx context.Context, r RecordRow) error {
	_, err := p.DB.ExecContext(ctx, `
		UPDATE dns_record
		   SET remote_record_id=$3, type=$4, name=$5, content=$6, ttl=$7, priority=$8, proxied=$9, updated_at=now()
		 WHERE workspace_id=$1 AND id=$2`,
		r.WorkspaceID, r.ID, r.RemoteRecordID, r.Type, r.Name, r.Content, r.TTL, priorityArg(r.Priority), r.Proxied)
	return err
}

// deleteRecord removes a cached record (after the provider delete
// succeeded). Returns false when no row matched.
func (p *Plugin) deleteRecord(ctx context.Context, wsID, recordID string) (bool, error) {
	res, err := p.DB.ExecContext(ctx, `DELETE FROM dns_record WHERE workspace_id=$1 AND id=$2`, wsID, recordID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func priorityArg(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
