# polar-dns

Unified **DNS control plane** for the Polar platform: one `/api/dns/*` surface
over multiple DNS providers (Name.com first, Cloudflare next), with a Provider
abstraction, per-provider HTTP proxy, encrypted credentials, audit log, and
(later) DNS-as-Code. Plan & design: [`doc/dev-plan.md`](doc/dev-plan.md),
[`doc/design.md`](doc/design.md).

Like every Polar module it owns its own database (`polar_dns`), validates user
sessions through dock (`polar-sdk`), and heartbeats into dock's plugin registry.
Status: **M0** — platform skeleton (no provider logic yet).

## Run (local)

```bash
# 1. database
createdb polar_dns   # or: psql -c "CREATE DATABASE polar_dns OWNER ideamesh;"
psql -d polar_dns -f scripts/migrate/dns-schema.sql

# 2. service (POLAR_PLUGIN_TOKEN is the plaintext from dock /admin-plugins.html)
POLAR_PLUGIN_TOKEN=<plaintext> \
POLAR_DNS_DB_DSN="postgres://ideamesh:test123456@127.0.0.1:5432/polar_dns?sslmode=disable" \
POLAR_DOCK_BASE="http://127.0.0.1:8080" \
make run

curl -s localhost:8096/healthz
```

## Env vars

| var | default | notes |
|---|---|---|
| `POLAR_PLUGIN_TOKEN` | — | **required**; plaintext from dock admin (fatal if unset) |
| `POLAR_DNS_DB_DSN` | `…/polar_dns?sslmode=disable` | connects only to `polar_dns` |
| `POLAR_DOCK_BASE` | `http://127.0.0.1:8080` | dock `/internal/v1/*` base |
| `POLAR_PLUGIN_NAME` | `dns` | must match `plugin_modules.name` in dock |
| `POLAR_DNS_LISTEN` | `127.0.0.1:8096` | |
| `POLAR_DNS_VERSION` | `0.0.1` | surfaced on heartbeat + `/healthz` |
| `POLAR_DNS_METRICS_TOKEN` | — | Bearer for `/metrics` (unset → 404) |
| `DNS_CRED_KEY` | — | 64 hex chars (32 bytes) AES-256-GCM; unset → credentials in plaintext |

## Build / test

```bash
make tidy && make build && make vet
```
