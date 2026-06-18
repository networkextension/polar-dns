package dns

// provider_local.go — the "local" provider: a self-hosted authoritative
// backend with NO remote DNS service. Every remote operation is a no-op, so
// records created under a local provider live purely in the dns_record cache
// (this module's DB). It turns polar-dns into a manager of self-hosted zones
// — the records a split-horizon resolver (polar-dns-resolver) renders into
// BIND/Unbound. See modules/polar-dns-resolver/doc/sync-design.md §1.
//
// A local provider needs no credentials and no proxy. Zones are NOT
// discoverable via sync (ListZones returns nothing); they are created
// explicitly via POST /api/dns/zones.

import "context"

const localType = "local"

func init() { registerProvider(localType, newLocalProvider) }

// localProvider holds no state — all its methods are no-ops over the local
// cache. cred/proxyURL are accepted (to satisfy the Factory signature) and
// ignored.
type localProvider struct{}

func newLocalProvider(_ map[string]string, _ string) (Provider, error) {
	return &localProvider{}, nil
}

func (p *localProvider) Type() string               { return localType }
func (p *localProvider) Capabilities() Capabilities { return Capabilities{} }

// ListZones/ListRecords: nothing to discover — a local provider has no remote
// to enumerate. (Sync over a local provider is therefore a no-op.)
func (p *localProvider) ListZones(_ context.Context) ([]Zone, error) { return nil, nil }
func (p *localProvider) ListRecords(_ context.Context, _ string) ([]Record, error) {
	return nil, nil
}

// CreateRecord mints a unique RemoteID. dns_record has UNIQUE(zone_id,
// remote_record_id); returning "" would collide on the second record in a
// zone, so every local record gets its own loc_ handle.
func (p *localProvider) CreateRecord(_ context.Context, _ string, r Record) (Record, error) {
	r.RemoteID = newID("loc_")
	return r, nil
}

// UpdateRecord keeps the record as-is (RemoteID unchanged). DeleteRecord is a
// no-op: there is no remote to delete from.
func (p *localProvider) UpdateRecord(_ context.Context, _ string, r Record) (Record, error) {
	return r, nil
}
func (p *localProvider) DeleteRecord(_ context.Context, _, _ string) error { return nil }
