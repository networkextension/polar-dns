package dns

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRetryTransport_RetriesIdempotentGET(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cl, _ := newHTTPClient("", 5*time.Second)
	resp, err := cl.Get(srv.URL)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("expected eventual 200, got resp=%v err=%v", resp, err)
	}
	if got := atomic.LoadInt32(&n); got != 3 {
		t.Fatalf("expected 3 attempts (2 retries), got %d", got)
	}
}

func TestRetryTransport_DoesNotRetryWrites(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	cl, _ := newHTTPClient("", 5*time.Second)
	resp, _ := cl.Post(srv.URL, "application/json", nil)
	if resp != nil {
		resp.Body.Close()
	}
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("POST must not be retried; got %d attempts", got)
	}
}

// fakeProvider implements Provider for the metered wrapper test.
type fakeProvider struct{ typ string }

func (f *fakeProvider) Type() string               { return f.typ }
func (f *fakeProvider) Capabilities() Capabilities { return Capabilities{Proxied: true} }
func (f *fakeProvider) ListZones(context.Context) ([]Zone, error) {
	return []Zone{{RemoteID: "z", Name: "e.com"}}, nil
}
func (f *fakeProvider) ListRecords(context.Context, string) ([]Record, error) { return nil, nil }
func (f *fakeProvider) CreateRecord(context.Context, string, Record) (Record, error) {
	return Record{}, nil
}
func (f *fakeProvider) UpdateRecord(context.Context, string, Record) (Record, error) {
	return Record{}, nil
}
func (f *fakeProvider) DeleteRecord(context.Context, string, string) error { return nil }

func TestMeteredProvider_PassthroughAndCount(t *testing.T) {
	m := newDNSMetrics()
	mp := newMeteredProvider(&fakeProvider{typ: "namecom"}, m)
	if mp.Type() != "namecom" || !mp.Capabilities().Proxied {
		t.Fatal("metered wrapper must delegate Type/Capabilities")
	}
	z, err := mp.ListZones(context.Background())
	if err != nil || len(z) != 1 || z[0].Name != "e.com" {
		t.Fatalf("ListZones not passed through: %v %v", z, err)
	}
	if got := testutil.ToFloat64(m.provReq.WithLabelValues("namecom", "list_zones", "ok")); got != 1 {
		t.Fatalf("expected 1 metered call, got %v", got)
	}
}

func TestNewMeteredProvider_NilMetricsReturnsInner(t *testing.T) {
	fp := &fakeProvider{typ: "x"}
	if newMeteredProvider(fp, nil) != Provider(fp) {
		t.Fatal("nil metrics should return the inner provider unwrapped")
	}
}
