package dns

// provider_cloudflare.go — Cloudflare API v4 provider (M4, second provider).
// Docs: https://developers.cloudflare.com/api/. Auth is a Bearer API token.
//
// Two CF-isms the abstraction has to hide:
//   - Zones are addressed by an OPAQUE id (not the apex domain). The neutral
//     model + Name.com use the apex name as the zone handle, but here the
//     zone RemoteID is the opaque id.
//   - Record names are FULL FQDNs ("www.example.com", apex = "example.com"),
//     whereas the neutral Record.Name is the RELATIVE host ("www", apex = "").
//     We convert both ways, which needs the zone's apex name — resolved from
//     the opaque id via a lazily-filled cache (populated by ListZones, or a
//     GET /zones/{id} on first standalone record op).
//
// Cloudflare DOES support per-record proxy (orange-cloud), so
// Capabilities().Proxied is true — this is what M4 exercises.
//
// Credential shape: {"api_token": "..."} (alias "token"). Optional
// {"base_url": "..."} for tests.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	cloudflareType    = "cloudflare"
	cloudflareBaseURL = "https://api.cloudflare.com/client/v4"
	cloudflarePerPage = 100
)

func init() { registerProvider(cloudflareType, newCloudflareProvider) }

type cloudflareProvider struct {
	token     string
	base      string
	client    *http.Client
	zoneNames map[string]string // zoneID -> apex name (lazy)
}

func newCloudflareProvider(cred map[string]string, proxyURL string) (Provider, error) {
	token := strings.TrimSpace(cred["api_token"])
	if token == "" {
		token = strings.TrimSpace(cred["token"])
	}
	if token == "" {
		return nil, errors.New("cloudflare: credential requires api_token")
	}
	client, err := newHTTPClient(proxyURL, 15*time.Second)
	if err != nil {
		return nil, err
	}
	base := cloudflareBaseURL
	if b := strings.TrimSpace(cred["base_url"]); b != "" {
		base = strings.TrimRight(b, "/")
	}
	return &cloudflareProvider{token: token, base: base, client: client, zoneNames: map[string]string{}}, nil
}

func (p *cloudflareProvider) Type() string               { return cloudflareType }
func (p *cloudflareProvider) Capabilities() Capabilities { return Capabilities{Proxied: true} }

// --- wire types ---

type cfZone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type cfRecord struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl,omitempty"`
	Priority *int   `json:"priority,omitempty"`
	Proxied  *bool  `json:"proxied,omitempty"`
}

type cfEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	ResultInfo struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
	Result json.RawMessage `json:"result"`
}

// --- name conversion (full FQDN <-> relative host) ---

func cfToRelative(fullName, zoneName string) string {
	fullName = strings.TrimSuffix(strings.TrimSpace(fullName), ".")
	if fullName == zoneName || fullName == "" {
		return ""
	}
	return strings.TrimSuffix(fullName, "."+zoneName)
}

func cfToFull(relHost, zoneName string) string {
	relHost = normalizeHost(relHost)
	if relHost == "" {
		return zoneName
	}
	return relHost + "." + zoneName
}

// --- mapping ---

func cfFromRecord(cf cfRecord, zoneName string) Record {
	r := Record{
		RemoteID: cf.ID,
		Type:     cf.Type,
		Name:     cfToRelative(cf.Name, zoneName),
		Content:  cf.Content,
		TTL:      cf.TTL,
	}
	if cf.Priority != nil {
		v := *cf.Priority
		r.Priority = &v
	}
	if cf.Proxied != nil {
		r.Proxied = *cf.Proxied
	}
	return r
}

func cfToRecord(r Record, zoneName string) cfRecord {
	cf := cfRecord{
		Type:    strings.ToUpper(strings.TrimSpace(r.Type)),
		Name:    cfToFull(r.Name, zoneName),
		Content: r.Content,
		TTL:     r.TTL,
	}
	if cf.TTL <= 0 {
		cf.TTL = 1 // Cloudflare: 1 == automatic
	}
	if r.Priority != nil {
		v := *r.Priority
		cf.Priority = &v
	}
	proxied := r.Proxied
	cf.Proxied = &proxied
	return cf
}

// --- Provider impl ---

func (p *cloudflareProvider) ListZones(ctx context.Context) ([]Zone, error) {
	var zones []Zone
	page := 1
	for {
		var env cfEnvelope
		path := fmt.Sprintf("/zones?per_page=%d&page=%d", cloudflarePerPage, page)
		if err := p.do(ctx, http.MethodGet, path, nil, &env); err != nil {
			return nil, err
		}
		var batch []cfZone
		if err := json.Unmarshal(env.Result, &batch); err != nil {
			return nil, fmt.Errorf("cloudflare: decode zones: %w", err)
		}
		for _, z := range batch {
			p.zoneNames[z.ID] = z.Name
			zones = append(zones, Zone{RemoteID: z.ID, Name: z.Name})
		}
		if env.ResultInfo.TotalPages == 0 || page >= env.ResultInfo.TotalPages {
			break
		}
		page++
	}
	return zones, nil
}

func (p *cloudflareProvider) ListRecords(ctx context.Context, zoneRemoteID string) ([]Record, error) {
	zoneRemoteID = strings.TrimSpace(zoneRemoteID)
	if zoneRemoteID == "" {
		return nil, errors.New("cloudflare: empty zone")
	}
	zoneName, err := p.zoneName(ctx, zoneRemoteID)
	if err != nil {
		return nil, err
	}
	var records []Record
	page := 1
	for {
		var env cfEnvelope
		path := fmt.Sprintf("/zones/%s/dns_records?per_page=%d&page=%d", url.PathEscape(zoneRemoteID), cloudflarePerPage, page)
		if err := p.do(ctx, http.MethodGet, path, nil, &env); err != nil {
			return nil, err
		}
		var batch []cfRecord
		if err := json.Unmarshal(env.Result, &batch); err != nil {
			return nil, fmt.Errorf("cloudflare: decode records: %w", err)
		}
		for _, cf := range batch {
			records = append(records, cfFromRecord(cf, zoneName))
		}
		if env.ResultInfo.TotalPages == 0 || page >= env.ResultInfo.TotalPages {
			break
		}
		page++
	}
	return records, nil
}

func (p *cloudflareProvider) CreateRecord(ctx context.Context, zoneRemoteID string, r Record) (Record, error) {
	zoneName, err := p.zoneName(ctx, zoneRemoteID)
	if err != nil {
		return Record{}, err
	}
	var out cfRecord
	path := fmt.Sprintf("/zones/%s/dns_records", url.PathEscape(strings.TrimSpace(zoneRemoteID)))
	if err := p.doResult(ctx, http.MethodPost, path, cfToRecord(r, zoneName), &out); err != nil {
		return Record{}, err
	}
	return cfFromRecord(out, zoneName), nil
}

func (p *cloudflareProvider) UpdateRecord(ctx context.Context, zoneRemoteID string, r Record) (Record, error) {
	id := strings.TrimSpace(r.RemoteID)
	if strings.TrimSpace(zoneRemoteID) == "" || id == "" {
		return Record{}, errors.New("cloudflare: update requires zone + record id")
	}
	zoneName, err := p.zoneName(ctx, zoneRemoteID)
	if err != nil {
		return Record{}, err
	}
	var out cfRecord
	path := fmt.Sprintf("/zones/%s/dns_records/%s", url.PathEscape(strings.TrimSpace(zoneRemoteID)), url.PathEscape(id))
	if err := p.doResult(ctx, http.MethodPut, path, cfToRecord(r, zoneName), &out); err != nil {
		return Record{}, err
	}
	return cfFromRecord(out, zoneName), nil
}

func (p *cloudflareProvider) DeleteRecord(ctx context.Context, zoneRemoteID, recordRemoteID string) error {
	if strings.TrimSpace(zoneRemoteID) == "" || strings.TrimSpace(recordRemoteID) == "" {
		return errors.New("cloudflare: delete requires zone + record id")
	}
	path := fmt.Sprintf("/zones/%s/dns_records/%s",
		url.PathEscape(strings.TrimSpace(zoneRemoteID)), url.PathEscape(strings.TrimSpace(recordRemoteID)))
	return p.do(ctx, http.MethodDelete, path, nil, nil)
}

// zoneName resolves a zone's apex name from its opaque id, caching it.
func (p *cloudflareProvider) zoneName(ctx context.Context, zoneID string) (string, error) {
	zoneID = strings.TrimSpace(zoneID)
	if n, ok := p.zoneNames[zoneID]; ok {
		return n, nil
	}
	var z cfZone
	if err := p.doResult(ctx, http.MethodGet, "/zones/"+url.PathEscape(zoneID), nil, &z); err != nil {
		return "", err
	}
	p.zoneNames[zoneID] = z.Name
	return z.Name, nil
}

// doResult is do() that also unmarshals the envelope's result into out.
func (p *cloudflareProvider) doResult(ctx context.Context, method, path string, body, out any) error {
	var env cfEnvelope
	if err := p.do(ctx, method, path, body, &env); err != nil {
		return err
	}
	if out != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("cloudflare: decode result %s %s: %w", method, path, err)
		}
	}
	return nil
}

// do sends a request, decoding the CF envelope into env (when non-nil) and
// turning success=false / non-2xx into an error.
func (p *cloudflareProvider) do(ctx context.Context, method, path string, body, env any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("cloudflare marshal: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))

	var probe cfEnvelope
	_ = json.Unmarshal(raw, &probe)
	if resp.StatusCode/100 != 2 || !probe.Success {
		msg := cfErrorMsg(probe)
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return fmt.Errorf("cloudflare %s %s: HTTP %d: %s", method, path, resp.StatusCode, msg)
	}
	if env != nil {
		if err := json.Unmarshal(raw, env); err != nil {
			return fmt.Errorf("cloudflare decode %s %s: %w", method, path, err)
		}
	}
	return nil
}

func cfErrorMsg(env cfEnvelope) string {
	parts := make([]string, 0, len(env.Errors))
	for _, e := range env.Errors {
		parts = append(parts, fmt.Sprintf("%d %s", e.Code, e.Message))
	}
	return strings.Join(parts, "; ")
}
