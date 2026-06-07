package dns

// provider_metered.go — a Provider decorator that records Prometheus
// metrics (call count + latency, by provider/op/outcome) around every
// upstream call. buildProvider wraps the real provider in this so the
// provider implementations stay free of metrics plumbing.

import (
	"context"
	"time"
)

type meteredProvider struct {
	inner Provider
	m     *dnsMetrics
}

func newMeteredProvider(inner Provider, m *dnsMetrics) Provider {
	if m == nil || inner == nil {
		return inner
	}
	return &meteredProvider{inner: inner, m: m}
}

func (mp *meteredProvider) Type() string               { return mp.inner.Type() }
func (mp *meteredProvider) Capabilities() Capabilities { return mp.inner.Capabilities() }

func (mp *meteredProvider) observe(op string, start time.Time, err error) {
	status := "ok"
	if err != nil {
		status = "error"
	}
	mp.m.provReq.WithLabelValues(mp.inner.Type(), op, status).Inc()
	mp.m.provDur.WithLabelValues(mp.inner.Type(), op).Observe(time.Since(start).Seconds())
}

func (mp *meteredProvider) ListZones(ctx context.Context) ([]Zone, error) {
	s := time.Now()
	z, err := mp.inner.ListZones(ctx)
	mp.observe("list_zones", s, err)
	return z, err
}

func (mp *meteredProvider) ListRecords(ctx context.Context, zoneRemoteID string) ([]Record, error) {
	s := time.Now()
	r, err := mp.inner.ListRecords(ctx, zoneRemoteID)
	mp.observe("list_records", s, err)
	return r, err
}

func (mp *meteredProvider) CreateRecord(ctx context.Context, zoneRemoteID string, r Record) (Record, error) {
	s := time.Now()
	out, err := mp.inner.CreateRecord(ctx, zoneRemoteID, r)
	mp.observe("create_record", s, err)
	return out, err
}

func (mp *meteredProvider) UpdateRecord(ctx context.Context, zoneRemoteID string, r Record) (Record, error) {
	s := time.Now()
	out, err := mp.inner.UpdateRecord(ctx, zoneRemoteID, r)
	mp.observe("update_record", s, err)
	return out, err
}

func (mp *meteredProvider) DeleteRecord(ctx context.Context, zoneRemoteID, recordRemoteID string) error {
	s := time.Now()
	err := mp.inner.DeleteRecord(ctx, zoneRemoteID, recordRemoteID)
	mp.observe("delete_record", s, err)
	return err
}
