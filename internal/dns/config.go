package dns

// Config is the dns-svc boot configuration, sourced from env in
// cmd/dns-svc/main.go. Kept deliberately small for the M0 skeleton;
// provider/proxy settings live per-row in dns_provider, not here.
type Config struct {
	DBDSN        string // POLAR_DNS_DB_DSN — connects ONLY to polar_dns
	DockBase     string // POLAR_DOCK_BASE — e.g. http://127.0.0.1:8080
	PluginName   string // POLAR_PLUGIN_NAME — must match plugin_modules.name in dock
	PluginToken  string // POLAR_PLUGIN_TOKEN — plaintext shown once by dock admin
	Listen       string // POLAR_DNS_LISTEN — e.g. 127.0.0.1:8096
	BuildVersion string // POLAR_DNS_VERSION
	MetricsToken string // POLAR_DNS_METRICS_TOKEN — Bearer for /metrics (empty → 404)
	CredKeyHex   string // DNS_CRED_KEY — 64 hex chars (32 bytes) AES-256-GCM key
	// PublicBaseURL is this plugin's externally reachable origin
	// (POLAR_DNS_PUBLIC_BASE_URL, e.g. https://dns.dev.4950.store). Sent on
	// heartbeat so dock can build the cross-subdomain sidebar link.
	PublicBaseURL string
}
