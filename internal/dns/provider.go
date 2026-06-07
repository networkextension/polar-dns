package dns

// provider.go — the provider abstraction. Every DNS backend (Name.com,
// Cloudflare, future Route53/DNSPod/AliDNS) implements Provider; the
// rest of the plugin speaks only this neutral interface. Capability bits
// let the API/UI layer hide or reject fields a given backend can't honor
// (e.g. Cloudflare's `proxied`) instead of the abstraction silently
// dropping them. See doc/dev-plan.md §3.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Record is a provider-neutral DNS record.
type Record struct {
	RemoteID string // provider's record handle ("" when creating)
	Type     string // A/AAAA/CNAME/TXT/MX/...
	Name     string // host relative to the zone; "" or "@" = apex
	Content  string // the record value (IP, target, text, ...)
	TTL      int
	Priority *int // MX/SRV only; nil when N/A
	Proxied  bool // Cloudflare orange-cloud; ignored by providers without the capability
}

// Zone is a provider-neutral DNS zone.
type Zone struct {
	RemoteID string // provider's zone handle (CF: opaque id; Name.com: apex domain)
	Name     string // apex domain, e.g. example.com
}

// Capabilities declares which optional features a provider supports.
type Capabilities struct {
	Proxied bool // supports per-record proxy/orange-cloud (Cloudflare)
}

// Provider is the contract every DNS backend implements. Zone is
// addressed by its provider-side RemoteID throughout.
type Provider interface {
	Type() string
	Capabilities() Capabilities
	ListZones(ctx context.Context) ([]Zone, error)
	ListRecords(ctx context.Context, zoneRemoteID string) ([]Record, error)
	CreateRecord(ctx context.Context, zoneRemoteID string, r Record) (Record, error)
	UpdateRecord(ctx context.Context, zoneRemoteID string, r Record) (Record, error)
	DeleteRecord(ctx context.Context, zoneRemoteID, recordRemoteID string) error
}

// Factory builds a Provider from decrypted credentials + an optional
// proxy URL (empty = direct).
type Factory func(cred map[string]string, proxyURL string) (Provider, error)

// registry maps provider_type → Factory. Populated by each provider's
// init(); read-only after package init.
var registry = map[string]Factory{}

func registerProvider(typ string, f Factory) { registry[typ] = f }

// NewProvider constructs a provider of the given type.
func NewProvider(typ string, cred map[string]string, proxyURL string) (Provider, error) {
	f, ok := registry[strings.TrimSpace(typ)]
	if !ok {
		return nil, fmt.Errorf("unknown provider type %q", typ)
	}
	return f(cred, proxyURL)
}

// ListProviderTypes returns the registered provider type names, sorted.
func ListProviderTypes() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// newHTTPClient builds an http.Client honoring an optional proxy. Empty
// proxyURL → direct. The scheme is validated (http/https/socks5 only) so
// a stored proxy_url can't be pointed at an arbitrary internal scheme.
func newHTTPClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return &http.Client{Timeout: timeout, Transport: withRetries(http.DefaultTransport)}, nil
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("proxy url: %w", err)
	}
	switch u.Scheme {
	case "http", "https", "socks5":
		// supported by net/http's Transport.Proxy
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q (want http/https/socks5)", u.Scheme)
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: withRetries(&http.Transport{Proxy: http.ProxyURL(u)}),
	}, nil
}

// retryTransport retries idempotent GET requests on transient failures
// (network errors, 429, 5xx) with linear backoff. Non-GET requests pass
// straight through — record writes must not be silently retried.
type retryTransport struct {
	base http.RoundTripper
	max  int
}

func withRetries(base http.RoundTripper) http.RoundTripper {
	return &retryTransport{base: base, max: 2}
}

func (rt *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return rt.base.RoundTrip(req)
	}
	var resp *http.Response
	var err error
	for attempt := 0; ; attempt++ {
		resp, err = rt.base.RoundTrip(req)
		if err == nil && resp.StatusCode != 429 && resp.StatusCode < 500 {
			return resp, nil
		}
		if attempt >= rt.max {
			return resp, err
		}
		if resp != nil {
			resp.Body.Close()
		}
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(time.Duration(attempt+1) * 200 * time.Millisecond):
		}
	}
}

// normalizeHost maps the apex sentinel "@" to the empty host that most
// provider APIs expect for apex records.
func normalizeHost(name string) string {
	name = strings.TrimSpace(name)
	if name == "@" {
		return ""
	}
	return name
}
