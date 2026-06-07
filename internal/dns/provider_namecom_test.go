package dns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newNamecomTestProvider(t *testing.T, h http.Handler) Provider {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	p, err := NewProvider(namecomType, map[string]string{
		"username": "user",
		"token":    "tok",
		"base_url": srv.URL,
	}, "")
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return p
}

func TestNamecomNeedsCredentials(t *testing.T) {
	if _, err := NewProvider(namecomType, map[string]string{"username": "u"}, ""); err == nil {
		t.Fatal("expected error when token missing")
	}
}

func TestNamecomCapabilitiesNoProxy(t *testing.T) {
	p := newNamecomTestProvider(t, http.NotFoundHandler())
	if p.Capabilities().Proxied {
		t.Fatal("namecom must not advertise proxied capability")
	}
	if p.Type() != namecomType {
		t.Fatalf("Type()=%q", p.Type())
	}
}

func TestNamecomListZonesPaginated(t *testing.T) {
	h := http.NewServeMux()
	h.HandleFunc("/v4/domains", func(w http.ResponseWriter, r *http.Request) {
		// verify Basic auth is sent
		if u, pw, ok := r.BasicAuth(); !ok || u != "user" || pw != "tok" {
			t.Errorf("missing/wrong basic auth: %q %q ok=%v", u, pw, ok)
		}
		switch r.URL.Query().Get("page") {
		case "1":
			json.NewEncoder(w).Encode(namecomListDomainsResp{
				Domains:  []namecomDomain{{DomainName: "a.com"}},
				NextPage: 2,
			})
		default:
			json.NewEncoder(w).Encode(namecomListDomainsResp{
				Domains:  []namecomDomain{{DomainName: "b.com"}},
				NextPage: 0,
			})
		}
	})
	p := newNamecomTestProvider(t, h)
	zones, err := p.ListZones(context.Background())
	if err != nil {
		t.Fatalf("ListZones: %v", err)
	}
	if len(zones) != 2 || zones[0].Name != "a.com" || zones[1].RemoteID != "b.com" {
		t.Fatalf("unexpected zones: %+v", zones)
	}
}

func TestNamecomListRecordsMapping(t *testing.T) {
	h := http.NewServeMux()
	h.HandleFunc("/v4/domains/example.com/records", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(namecomListRecordsResp{
			Records: []namecomRecord{
				{ID: 11, Host: "www", Type: "A", Answer: "1.1.1.1", TTL: 300},
				{ID: 12, Host: "", Type: "MX", Answer: "mail.example.com", TTL: 3600, Priority: 10},
			},
		})
	})
	p := newNamecomTestProvider(t, h)
	recs, err := p.ListRecords(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d", len(recs))
	}
	if recs[0].RemoteID != "11" || recs[0].Name != "www" || recs[0].Content != "1.1.1.1" {
		t.Fatalf("record0 mapping wrong: %+v", recs[0])
	}
	if recs[1].Priority == nil || *recs[1].Priority != 10 {
		t.Fatalf("MX priority not mapped: %+v", recs[1])
	}
	if recs[0].Priority != nil {
		t.Fatalf("A record should have nil priority, got %v", *recs[0].Priority)
	}
}

func TestNamecomCreateRecordSendsMappedBody(t *testing.T) {
	var gotBody namecomRecord
	h := http.NewServeMux()
	h.HandleFunc("/v4/domains/example.com/records", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("want POST, got %s", r.Method)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		gotBody.ID = 99 // server assigns id
		json.NewEncoder(w).Encode(gotBody)
	})
	p := newNamecomTestProvider(t, h)
	pr := 20
	out, err := p.CreateRecord(context.Background(), "example.com", Record{
		Type: "mx", Name: "@", Content: "mail.example.com", TTL: 600, Priority: &pr,
	})
	if err != nil {
		t.Fatalf("CreateRecord: %v", err)
	}
	if gotBody.Host != "" {
		t.Errorf("apex '@' should map to empty host, got %q", gotBody.Host)
	}
	if gotBody.Type != "MX" {
		t.Errorf("type should be upper-cased, got %q", gotBody.Type)
	}
	if gotBody.Priority != 20 {
		t.Errorf("priority not sent: %d", gotBody.Priority)
	}
	if out.RemoteID != "99" {
		t.Errorf("created record id not returned: %+v", out)
	}
}

func TestNamecomUpdateAndDelete(t *testing.T) {
	var deleted bool
	h := http.NewServeMux()
	h.HandleFunc("/v4/domains/example.com/records/5", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			var in namecomRecord
			json.NewDecoder(r.Body).Decode(&in)
			in.ID = 5
			json.NewEncoder(w).Encode(in)
		case http.MethodDelete:
			deleted = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected method %s", r.Method)
		}
	})
	p := newNamecomTestProvider(t, h)
	out, err := p.UpdateRecord(context.Background(), "example.com", Record{
		RemoteID: "5", Type: "A", Name: "www", Content: "2.2.2.2", TTL: 120,
	})
	if err != nil {
		t.Fatalf("UpdateRecord: %v", err)
	}
	if out.Content != "2.2.2.2" || out.RemoteID != "5" {
		t.Fatalf("update result wrong: %+v", out)
	}
	if err := p.DeleteRecord(context.Background(), "example.com", "5"); err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}
	if !deleted {
		t.Fatal("delete handler not hit")
	}
}

func TestNamecomErrorMapping(t *testing.T) {
	h := http.NewServeMux()
	h.HandleFunc("/v4/domains", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(namecomError{Message: "Permission Denied", Details: "bad token"})
	})
	p := newNamecomTestProvider(t, h)
	_, err := p.ListZones(context.Background())
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "Permission Denied") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("error should surface status + message, got: %v", err)
	}
}
