-- ============================================================
-- polar_dns schema — end-state (M0).
--
-- Apply:
--   CREATE DATABASE polar_dns OWNER ideamesh;
--   psql -d polar_dns -f scripts/migrate/dns-schema.sql
--
-- The dns plugin owns the unified DNS control plane: dns_provider
-- (per-workspace provider accounts + encrypted credentials), dns_zone
-- (zones mirrored from a provider), dns_record (record cache; the remote
-- provider is the source of truth), and dns_audit_log (who changed what).
--
-- Cross-DB references (TEXT, resolved via dock SDK — NO foreign keys):
--   - created_by, actor_user_id  → /internal/v1/users/:id
--   - workspace_id               → /internal/v1/teams/:id
--
-- Every user-visible table carries workspace_id (multi-tenant partition).
-- IDs are TEXT with a domain prefix (dp_/dz_/dr_), minted by the service.
-- Idempotent: safe to re-run (CREATE ... IF NOT EXISTS).
-- ============================================================

-- Provider accounts. credential is sealed with DNS_CRED_KEY (AES-256-GCM)
-- into cred_cipher; when the key is unset it degrades to cred_plain +
-- encrypted=false (UI surfaces the inactive-encryption badge).
CREATE TABLE IF NOT EXISTS dns_provider (
    id            TEXT PRIMARY KEY,              -- dp_<rand>
    workspace_id  TEXT NOT NULL,
    name          TEXT NOT NULL,                 -- display name, e.g. "name-main"
    provider_type TEXT NOT NULL,                 -- namecom | cloudflare | (future) route53...
    cred_cipher   TEXT NOT NULL DEFAULT '',      -- AES-256-GCM(JSON credential), base64
    cred_plain    TEXT NOT NULL DEFAULT '',      -- plaintext fallback when key absent
    encrypted     BOOLEAN NOT NULL DEFAULT FALSE,
    proxy_url     TEXT NOT NULL DEFAULT '',      -- e.g. http://127.0.0.1:7890
    created_by    TEXT NOT NULL,                 -- user_id (resolved via dock)
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, name)
);

-- Zones mirrored from a provider. remote_zone_id is the provider's handle
-- (Cloudflare: opaque id; Name.com: the apex domain).
CREATE TABLE IF NOT EXISTS dns_zone (
    id             TEXT PRIMARY KEY,             -- dz_<rand>
    workspace_id   TEXT NOT NULL,
    provider_id    TEXT NOT NULL REFERENCES dns_provider(id) ON DELETE CASCADE,
    zone_name      TEXT NOT NULL,                -- example.com
    remote_zone_id TEXT NOT NULL,
    serial         BIGINT NOT NULL DEFAULT 1,    -- monotonic per-zone version; bumped on every
                                                 -- record write-through. Consumed by the dns
                                                 -- resolver (BIND SOA serial). Meaningful for
                                                 -- 'local' zones; harmless elsewhere.
    last_synced_at TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, provider_id, zone_name)
);

-- Record cache. The remote provider is authoritative; writes go through
-- the provider first, then update this row.
CREATE TABLE IF NOT EXISTS dns_record (
    id               TEXT PRIMARY KEY,           -- dr_<rand>
    workspace_id     TEXT NOT NULL,
    zone_id          TEXT NOT NULL REFERENCES dns_zone(id) ON DELETE CASCADE,
    remote_record_id TEXT NOT NULL DEFAULT '',
    type             TEXT NOT NULL,              -- A/AAAA/CNAME/TXT/MX/...
    name             TEXT NOT NULL,              -- subdomain (or @)
    content          TEXT NOT NULL,
    ttl              INTEGER NOT NULL DEFAULT 300,
    priority         INTEGER,                    -- MX/SRV
    proxied          BOOLEAN NOT NULL DEFAULT FALSE,  -- Cloudflare-only; ignored elsewhere
    view             TEXT NOT NULL DEFAULT 'any',     -- any|public|private; split-horizon dimension
                                                      -- for 'local' zones. Other providers ignore it.
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (zone_id, remote_record_id)
);
CREATE INDEX IF NOT EXISTS idx_dns_record_zone ON dns_record(zone_id);

-- Additive columns for existing databases (CREATE TABLE IF NOT EXISTS above is a
-- no-op once the table exists, so re-running the schema must ALTER in new columns).
ALTER TABLE dns_zone   ADD COLUMN IF NOT EXISTS serial BIGINT NOT NULL DEFAULT 1;
ALTER TABLE dns_record ADD COLUMN IF NOT EXISTS view   TEXT   NOT NULL DEFAULT 'any';

-- Audit trail: who, when, what, old/new.
CREATE TABLE IF NOT EXISTS dns_audit_log (
    id            BIGSERIAL PRIMARY KEY,
    workspace_id  TEXT NOT NULL,
    actor_user_id TEXT NOT NULL,
    action        TEXT NOT NULL,                 -- record.create|update|delete|provider.add|zone.sync
    target        TEXT NOT NULL DEFAULT '',      -- affected record/zone identifier
    old_value     JSONB,
    new_value     JSONB,
    at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_dns_audit_ws_at ON dns_audit_log(workspace_id, at DESC);
