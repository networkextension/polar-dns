package dns

// providers_store.go — persistence + credential sealing for dns_provider
// (provider accounts). Credentials are sealed with DNS_CRED_KEY when it's
// configured; otherwise stored plaintext with encrypted=false (the
// platform graceful-degradation pattern). The interface-level Provider
// (provider.go) is built from a decrypted account via buildProvider.

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ProviderAccount is a dns_provider row. The credential fields are
// unexported so they never leak through JSON responses.
type ProviderAccount struct {
	ID           string    `json:"id"`
	WorkspaceID  string    `json:"workspace_id"`
	Name         string    `json:"name"`
	ProviderType string    `json:"provider_type"`
	ProxyURL     string    `json:"proxy_url,omitempty"`
	Encrypted    bool      `json:"encrypted"`
	CreatedBy    string    `json:"created_by"`
	CreatedAt    time.Time `json:"created_at"`

	credCipher string
	credPlain  string
}

// sealCredential turns a credential map into stored columns. With a valid
// key → (cipher, "", true); without → ("", plaintextJSON, false).
func (p *Plugin) sealCredential(cred map[string]string) (cipher, plain string, encrypted bool, err error) {
	raw, err := json.Marshal(cred)
	if err != nil {
		return "", "", false, err
	}
	if len(p.polarCredentialKey) == dnsCredKeyBytes {
		c, e := seal(p.polarCredentialKey, string(raw))
		if e != nil {
			return "", "", false, e
		}
		return c, "", true, nil
	}
	return "", string(raw), false, nil
}

// openCredential reverses sealCredential for a stored account.
func (p *Plugin) openCredential(a ProviderAccount) (map[string]string, error) {
	var raw string
	if a.Encrypted {
		if len(p.polarCredentialKey) != dnsCredKeyBytes {
			return nil, errors.New("provider credential is encrypted but DNS_CRED_KEY is unavailable")
		}
		s, err := open(p.polarCredentialKey, a.credCipher)
		if err != nil {
			return nil, err
		}
		raw = s
	} else {
		raw = a.credPlain
	}
	m := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	return m, nil
}

// buildProvider constructs a live Provider from a stored account, wrapped
// with metrics instrumentation.
func (p *Plugin) buildProvider(a ProviderAccount) (Provider, error) {
	cred, err := p.openCredential(a)
	if err != nil {
		return nil, err
	}
	prov, err := NewProvider(a.ProviderType, cred, a.ProxyURL)
	if err != nil {
		return nil, err
	}
	return newMeteredProvider(prov, p.metrics), nil
}

func (p *Plugin) insertProvider(ctx context.Context, a ProviderAccount) error {
	_, err := p.DB.ExecContext(ctx, `
		INSERT INTO dns_provider
			(id, workspace_id, name, provider_type, cred_cipher, cred_plain, encrypted, proxy_url, created_by, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9, now())`,
		a.ID, a.WorkspaceID, a.Name, a.ProviderType, a.credCipher, a.credPlain, a.Encrypted, a.ProxyURL, a.CreatedBy)
	return err
}

func (p *Plugin) listProviders(ctx context.Context, wsID string) ([]ProviderAccount, error) {
	rows, err := p.DB.QueryContext(ctx, `
		SELECT id, workspace_id, name, provider_type, encrypted, proxy_url, created_by, created_at
		FROM dns_provider WHERE workspace_id=$1 ORDER BY created_at DESC`, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProviderAccount{}
	for rows.Next() {
		var a ProviderAccount
		if err := rows.Scan(&a.ID, &a.WorkspaceID, &a.Name, &a.ProviderType, &a.Encrypted, &a.ProxyURL, &a.CreatedBy, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// getProvider returns the full account (including credential columns) for
// a workspace-scoped id. Returns sql.ErrNoRows when absent.
func (p *Plugin) getProvider(ctx context.Context, wsID, id string) (ProviderAccount, error) {
	var a ProviderAccount
	err := p.DB.QueryRowContext(ctx, `
		SELECT id, workspace_id, name, provider_type, cred_cipher, cred_plain, encrypted, proxy_url, created_by, created_at
		FROM dns_provider WHERE workspace_id=$1 AND id=$2`, wsID, id).
		Scan(&a.ID, &a.WorkspaceID, &a.Name, &a.ProviderType, &a.credCipher, &a.credPlain, &a.Encrypted, &a.ProxyURL, &a.CreatedBy, &a.CreatedAt)
	return a, err
}

// updateProvider updates a provider's mutable fields (name, proxy, sealed
// credential columns). provider_type is immutable and not touched here.
// Returns false when no row matched.
func (p *Plugin) updateProvider(ctx context.Context, a ProviderAccount) (bool, error) {
	res, err := p.DB.ExecContext(ctx, `
		UPDATE dns_provider
		   SET name=$3, proxy_url=$4, cred_cipher=$5, cred_plain=$6, encrypted=$7
		 WHERE workspace_id=$1 AND id=$2`,
		a.WorkspaceID, a.ID, a.Name, a.ProxyURL, a.credCipher, a.credPlain, a.Encrypted)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// deleteProvider removes a workspace-scoped provider (zones/records cascade
// via FK). Returns false when no row matched.
func (p *Plugin) deleteProvider(ctx context.Context, wsID, id string) (bool, error) {
	res, err := p.DB.ExecContext(ctx, `DELETE FROM dns_provider WHERE workspace_id=$1 AND id=$2`, wsID, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
