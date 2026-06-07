package dns

import "github.com/prometheus/client_golang/prometheus"

// dnsMetrics holds the plugin's private Prometheus registry, served at
// /metrics behind DNS_METRICS_TOKEN. M0 ships only a liveness gauge;
// provider/sync/record counters land alongside their features.
type dnsMetrics struct {
	registry *prometheus.Registry
	upGauge  prometheus.Gauge
	provReq  *prometheus.CounterVec   // upstream provider API calls
	provDur  *prometheus.HistogramVec // upstream provider API latency
}

func newDNSMetrics() *dnsMetrics {
	m := &dnsMetrics{registry: prometheus.NewRegistry()}
	m.upGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "polar_dns_up",
		Help: "Always 1 while dns-svc is serving.",
	})
	m.provReq = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "polar_dns_provider_requests_total",
		Help: "Upstream DNS provider API calls by provider, operation, and outcome.",
	}, []string{"provider", "op", "status"})
	m.provDur = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "polar_dns_provider_request_duration_seconds",
		Help:    "Upstream DNS provider API call latency by provider and operation.",
		Buckets: prometheus.DefBuckets,
	}, []string{"provider", "op"})
	m.registry.MustRegister(m.upGauge, m.provReq, m.provDur)
	m.upGauge.Set(1)
	return m
}
