package dns

// audit_store.go — dns_audit_log writer. Best-effort: an audit write
// failure is logged but never fails the user's operation (the operation
// itself already succeeded). old/new are marshalled to JSONB.

import (
	"context"
	"encoding/json"
	"log"
	"time"
)

// AuditRow is one dns_audit_log entry for GET /api/dns/audit.
type AuditRow struct {
	ID          int64           `json:"id"`
	WorkspaceID string          `json:"workspace_id"`
	ActorUserID string          `json:"actor_user_id"`
	Action      string          `json:"action"`
	Target      string          `json:"target"`
	OldValue    json.RawMessage `json:"old_value,omitempty"`
	NewValue    json.RawMessage `json:"new_value,omitempty"`
	At          time.Time       `json:"at"`
}

// listAudit returns the most recent audit entries for a workspace.
func (p *Plugin) listAudit(ctx context.Context, wsID string, limit int) ([]AuditRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := p.DB.QueryContext(ctx, `
		SELECT id, workspace_id, actor_user_id, action, target, old_value, new_value, at
		FROM dns_audit_log WHERE workspace_id=$1 ORDER BY at DESC, id DESC LIMIT $2`, wsID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditRow{}
	for rows.Next() {
		var a AuditRow
		var oldV, newV []byte
		if err := rows.Scan(&a.ID, &a.WorkspaceID, &a.ActorUserID, &a.Action, &a.Target, &oldV, &newV, &a.At); err != nil {
			return nil, err
		}
		a.OldValue = json.RawMessage(oldV)
		a.NewValue = json.RawMessage(newV)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (p *Plugin) writeAudit(ctx context.Context, wsID, actorUserID, action, target string, oldV, newV any) {
	var ob, nb []byte
	if oldV != nil {
		ob, _ = json.Marshal(oldV)
	}
	if newV != nil {
		nb, _ = json.Marshal(newV)
	}
	_, err := p.DB.ExecContext(ctx, `
		INSERT INTO dns_audit_log (workspace_id, actor_user_id, action, target, old_value, new_value, at)
		VALUES ($1,$2,$3,$4,$5,$6, now())`,
		wsID, actorUserID, action, target, nullJSON(ob), nullJSON(nb))
	if err != nil {
		log.Printf("dns: audit write failed (action=%s target=%s): %v", action, target, err)
	}
}
