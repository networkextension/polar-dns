package dns

// provider_namecom.go — Name.com Core API v4 provider (the M1 first
// provider). Docs: https://www.name.com/api-docs/v4. Auth is HTTP Basic
// (username + API token). Zones are addressed by the apex domain name
// (Name.com has no opaque zone id). Name.com has no proxy/orange-cloud
// concept, so Capabilities().Proxied is false.
//
// Credential shape (cred map): {"username": "...", "token": "..."}.
// Optional {"base_url": "https://api.dev.name.com"} targets the sandbox
// (also used by tests to point at an httptest server).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	namecomType    = "namecom"
	namecomBaseURL = "https://api.name.com"
	namecomPerPage = 1000
)

func init() { registerProvider(namecomType, newNamecomProvider) }

type namecomProvider struct {
	username string
	token    string
	base     string
	client   *http.Client
}

func newNamecomProvider(cred map[string]string, proxyURL string) (Provider, error) {
	username := strings.TrimSpace(cred["username"])
	token := strings.TrimSpace(cred["token"])
	if username == "" || token == "" {
		return nil, errors.New("namecom: credential requires username + token")
	}
	client, err := newHTTPClient(proxyURL, 15*time.Second)
	if err != nil {
		return nil, err
	}
	base := namecomBaseURL
	if b := strings.TrimSpace(cred["base_url"]); b != "" {
		base = strings.TrimRight(b, "/")
	}
	return &namecomProvider{username: username, token: token, base: base, client: client}, nil
}

func (p *namecomProvider) Type() string               { return namecomType }
func (p *namecomProvider) Capabilities() Capabilities { return Capabilities{Proxied: false} }

// --- wire types ---

type namecomDomain struct {
	DomainName string `json:"domainName"`
}

type namecomListDomainsResp struct {
	Domains  []namecomDomain `json:"domains"`
	NextPage int             `json:"nextPage"`
}

type namecomRecord struct {
	ID         int32  `json:"id,omitempty"`
	DomainName string `json:"domainName,omitempty"`
	Host       string `json:"host,omitempty"`
	FQDN       string `json:"fqdn,omitempty"`
	Type       string `json:"type"`
	Answer     string `json:"answer"`
	TTL        uint32 `json:"ttl,omitempty"`
	Priority   uint32 `json:"priority,omitempty"`
}

type namecomListRecordsResp struct {
	Records  []namecomRecord `json:"records"`
	NextPage int             `json:"nextPage"`
}

type namecomError struct {
	Message string `json:"message"`
	Details string `json:"details"`
}

// --- mapping ---

func fromNamecomRecord(nr namecomRecord) Record {
	r := Record{
		RemoteID: strconv.Itoa(int(nr.ID)),
		Type:     nr.Type,
		Name:     nr.Host,
		Content:  nr.Answer,
		TTL:      int(nr.TTL),
	}
	if nr.Priority != 0 {
		pr := int(nr.Priority)
		r.Priority = &pr
	}
	return r
}

func toNamecomRecord(r Record) namecomRecord {
	nr := namecomRecord{
		Host:   normalizeHost(r.Name),
		Type:   strings.ToUpper(strings.TrimSpace(r.Type)),
		Answer: r.Content,
		TTL:    uint32(r.TTL),
	}
	if r.Priority != nil {
		nr.Priority = uint32(*r.Priority)
	}
	return nr
}

// --- Provider impl ---

func (p *namecomProvider) ListZones(ctx context.Context) ([]Zone, error) {
	var zones []Zone
	page := 1
	for {
		var resp namecomListDomainsResp
		path := fmt.Sprintf("/v4/domains?perPage=%d&page=%d", namecomPerPage, page)
		if err := p.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return nil, err
		}
		for _, d := range resp.Domains {
			zones = append(zones, Zone{RemoteID: d.DomainName, Name: d.DomainName})
		}
		if resp.NextPage == 0 {
			break
		}
		page = resp.NextPage
	}
	return zones, nil
}

func (p *namecomProvider) ListRecords(ctx context.Context, zoneRemoteID string) ([]Record, error) {
	domain := url.PathEscape(strings.TrimSpace(zoneRemoteID))
	if domain == "" {
		return nil, errors.New("namecom: empty zone")
	}
	var records []Record
	page := 1
	for {
		var resp namecomListRecordsResp
		path := fmt.Sprintf("/v4/domains/%s/records?perPage=%d&page=%d", domain, namecomPerPage, page)
		if err := p.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
			return nil, err
		}
		for _, nr := range resp.Records {
			records = append(records, fromNamecomRecord(nr))
		}
		if resp.NextPage == 0 {
			break
		}
		page = resp.NextPage
	}
	return records, nil
}

func (p *namecomProvider) CreateRecord(ctx context.Context, zoneRemoteID string, r Record) (Record, error) {
	domain := url.PathEscape(strings.TrimSpace(zoneRemoteID))
	if domain == "" {
		return Record{}, errors.New("namecom: empty zone")
	}
	var out namecomRecord
	path := fmt.Sprintf("/v4/domains/%s/records", domain)
	if err := p.do(ctx, http.MethodPost, path, toNamecomRecord(r), &out); err != nil {
		return Record{}, err
	}
	return fromNamecomRecord(out), nil
}

func (p *namecomProvider) UpdateRecord(ctx context.Context, zoneRemoteID string, r Record) (Record, error) {
	domain := url.PathEscape(strings.TrimSpace(zoneRemoteID))
	id := strings.TrimSpace(r.RemoteID)
	if domain == "" || id == "" {
		return Record{}, errors.New("namecom: update requires zone + record id")
	}
	var out namecomRecord
	path := fmt.Sprintf("/v4/domains/%s/records/%s", domain, url.PathEscape(id))
	if err := p.do(ctx, http.MethodPut, path, toNamecomRecord(r), &out); err != nil {
		return Record{}, err
	}
	return fromNamecomRecord(out), nil
}

func (p *namecomProvider) DeleteRecord(ctx context.Context, zoneRemoteID, recordRemoteID string) error {
	domain := url.PathEscape(strings.TrimSpace(zoneRemoteID))
	id := strings.TrimSpace(recordRemoteID)
	if domain == "" || id == "" {
		return errors.New("namecom: delete requires zone + record id")
	}
	path := fmt.Sprintf("/v4/domains/%s/records/%s", domain, url.PathEscape(id))
	return p.do(ctx, http.MethodDelete, path, nil, nil)
}

// do signs (Basic auth) + sends a request, decoding a 2xx body into out
// (when non-nil) or wrapping a 4xx/5xx body as a Name.com error.
func (p *namecomProvider) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("namecom marshal: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+path, rdr)
	if err != nil {
		return err
	}
	req.SetBasicAuth(p.username, p.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("namecom %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		var e namecomError
		_ = json.Unmarshal(raw, &e)
		msg := strings.TrimSpace(e.Message + " " + e.Details)
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return fmt.Errorf("namecom %s %s: HTTP %d: %s", method, path, resp.StatusCode, msg)
	}
	if out == nil {
		return nil
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("namecom decode %s %s: %w", method, path, err)
	}
	return nil
}
