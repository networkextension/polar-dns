package dns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newCFTestProvider(t *testing.T, h http.Handler) Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := NewProvider(cloudflareType, map[string]string{
		"api_token": "cf-tok",
		"base_url":  srv.URL,
	}, "")
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return p
}

// cfOK wraps a result in the CF success envelope.
func cfOK(w http.ResponseWriter, result any, totalPages int) {
	b, _ := json.Marshal(result)
	json.NewEncoder(w).Encode(map[string]any{
		"success":     true,
		"errors":      []any{},
		"result":      json.RawMessage(b),
		"result_info": map[string]int{"page": 1, "total_pages": totalPages},
	})
}

func TestCloudflareRegisteredWithProxyCapability(t *testing.T) {
	p := newCFTestProvider(t, http.NotFoundHandler())
	if !p.Capabilities().Proxied {
		t.Fatal("cloudflare must advertise proxied capability")
	}
	if p.Type() != cloudflareType {
		t.Fatalf("Type()=%q", p.Type())
	}
}

func TestCloudflareNeedsToken(t *testing.T) {
	if _, err := NewProvider(cloudflareType, map[string]string{}, ""); err == nil {
		t.Fatal("expected error without api_token")
	}
}

func TestCloudflareListZonesAndAuth(t *testing.T) {
	h := http.NewServeMux()
	h.HandleFunc("/zones", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cf-tok" {
			t.Errorf("missing/wrong bearer: %q", r.Header.Get("Authorization"))
		}
		cfOK(w, []cfZone{{ID: "zone123", Name: "example.com"}}, 1)
	})
	p := newCFTestProvider(t, h)
	zones, err := p.ListZones(context.Background())
	if err != nil {
		t.Fatalf("ListZones: %v", err)
	}
	if len(zones) != 1 || zones[0].RemoteID != "zone123" || zones[0].Name != "example.com" {
		t.Fatalf("unexpected zones: %+v", zones)
	}
}

func TestCloudflareListRecordsNameToRelative(t *testing.T) {
	h := http.NewServeMux()
	// zone name resolution (lazy)
	h.HandleFunc("/zones/zone123", func(w http.ResponseWriter, r *http.Request) {
		cfOK(w, cfZone{ID: "zone123", Name: "example.com"}, 0)
	})
	h.HandleFunc("/zones/zone123/dns_records", func(w http.ResponseWriter, r *http.Request) {
		tru := true
		cfOK(w, []cfRecord{
			{ID: "r1", Type: "A", Name: "www.example.com", Content: "1.1.1.1", TTL: 300, Proxied: &tru},
			{ID: "r2", Type: "A", Name: "example.com", Content: "2.2.2.2", TTL: 1},
		}, 1)
	})
	p := newCFTestProvider(t, h)
	recs, err := p.ListRecords(context.Background(), "zone123")
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d", len(recs))
	}
	if recs[0].Name != "www" { // FQDN -> relative host
		t.Fatalf("subdomain not relativized: %q", recs[0].Name)
	}
	if !recs[0].Proxied {
		t.Fatalf("proxied not mapped: %+v", recs[0])
	}
	if recs[1].Name != "" { // apex -> empty
		t.Fatalf("apex not relativized to empty: %q", recs[1].Name)
	}
}

func TestCloudflareCreateSendsFQDNAndProxied(t *testing.T) {
	var got cfRecord
	h := http.NewServeMux()
	h.HandleFunc("/zones/zone123", func(w http.ResponseWriter, r *http.Request) {
		cfOK(w, cfZone{ID: "zone123", Name: "example.com"}, 0)
	})
	h.HandleFunc("/zones/zone123/dns_records", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("want POST, got %s", r.Method)
		}
		json.NewDecoder(r.Body).Decode(&got)
		got.ID = "newrec"
		cfOK(w, got, 0)
	})
	p := newCFTestProvider(t, h)
	out, err := p.CreateRecord(context.Background(), "zone123", Record{
		Type: "A", Name: "www", Content: "1.1.1.1", TTL: 120, Proxied: true,
	})
	if err != nil {
		t.Fatalf("CreateRecord: %v", err)
	}
	if got.Name != "www.example.com" {
		t.Errorf("relative host should become FQDN, got %q", got.Name)
	}
	if got.Proxied == nil || !*got.Proxied {
		t.Errorf("proxied not sent: %+v", got.Proxied)
	}
	if out.RemoteID != "newrec" || out.Name != "www" {
		t.Errorf("create result mapping wrong: %+v", out)
	}
}

func TestCloudflareErrorEnvelope(t *testing.T) {
	h := http.NewServeMux()
	h.HandleFunc("/zones", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"errors":  []map[string]any{{"code": 9109, "message": "Invalid access token"}},
		})
	})
	p := newCFTestProvider(t, h)
	if _, err := p.ListZones(context.Background()); err == nil {
		t.Fatal("expected error on success=false")
	} else if !strings.Contains(err.Error(), "Invalid access token") {
		t.Fatalf("error should surface CF message, got: %v", err)
	}
}

func TestCFNameConversionHelpers(t *testing.T) {
	if cfToRelative("www.example.com", "example.com") != "www" {
		t.Fatal("www relativize")
	}
	if cfToRelative("example.com", "example.com") != "" {
		t.Fatal("apex relativize")
	}
	if cfToRelative("a.b.example.com", "example.com") != "a.b" {
		t.Fatal("nested relativize")
	}
	if cfToFull("www", "example.com") != "www.example.com" {
		t.Fatal("www full")
	}
	if cfToFull("@", "example.com") != "example.com" {
		t.Fatal("apex @ full")
	}
	if cfToFull("", "example.com") != "example.com" {
		t.Fatal("apex empty full")
	}
}
