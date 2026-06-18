// Package dns is the polar DNS control-plane plugin: a unified control
// plane over multiple DNS providers (Name.com first, Cloudflare next),
// exposing one /api/dns/* surface that hides the provider behind a
// Provider abstraction. See doc/dev-plan.md.
//
// Like every polar plugin it owns its own database (polar_dns), validates
// user sessions through dock's /internal/v1/auth/verify (via polar-sdk),
// and heartbeats into dock's plugin registry. Cross-domain user/team
// lookups go through the dock SDK; there are no cross-database foreign
// keys (workspace_id / created_by are TEXT pointers).
//
// M0 is platform wiring only — no provider logic yet.
package dns

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/networkextension/polar-sdk"
)

// dnsCredKeyBytes is the required DNS_CRED_KEY length (AES-256-GCM).
const dnsCredKeyBytes = 32

type Plugin struct {
	DB         *sql.DB
	Dock       *sdk.Client
	Name       string
	Listen     string
	Ver        string
	MetricsTok string
	PublicURL  string // externally reachable origin, sent on heartbeat

	// polarCredentialKey — AES-256-GCM key for dns_provider credentials.
	// Sourced from $DNS_CRED_KEY (hex). Empty/wrong-length means "store
	// plaintext" + the provider row's encrypted flag stays false.
	polarCredentialKey []byte

	metrics   *dnsMetrics
	startedAt time.Time
}

func New(ctx context.Context, cfg Config) (*Plugin, error) {
	cfg.PluginName = strings.TrimSpace(cfg.PluginName)
	if cfg.PluginName == "" {
		cfg.PluginName = "dns"
	}
	if strings.TrimSpace(cfg.DBDSN) == "" {
		return nil, errors.New("dns.New: DBDSN required")
	}
	if strings.TrimSpace(cfg.DockBase) == "" {
		return nil, errors.New("dns.New: DockBase required")
	}
	if strings.TrimSpace(cfg.PluginToken) == "" {
		return nil, errors.New("dns.New: PluginToken required")
	}

	db, err := sql.Open("postgres", cfg.DBDSN)
	if err != nil {
		return nil, fmt.Errorf("open polar_dns: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping polar_dns: %w", err)
	}

	var credentialKey []byte
	if k := strings.TrimSpace(cfg.CredKeyHex); k != "" {
		b, decErr := hex.DecodeString(k)
		if decErr != nil {
			log.Printf("dns: WARN DNS_CRED_KEY not valid hex: %v — credentials stored in plaintext", decErr)
		} else if len(b) != dnsCredKeyBytes {
			log.Printf("dns: WARN DNS_CRED_KEY length=%d want=%d — credentials stored in plaintext", len(b), dnsCredKeyBytes)
		} else {
			credentialKey = b
		}
	} else {
		log.Print("dns: DNS_CRED_KEY not set — dns_provider credentials stored in plaintext")
	}

	dock := sdk.NewClient(cfg.DockBase, cfg.PluginName, sdk.DeriveHMACKey(cfg.PluginToken))
	resp, err := dock.Do(http.MethodGet, "/internal/v1/ping", nil)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("dock ping: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = db.Close()
		return nil, fmt.Errorf("dock /ping rejected: HTTP %d", resp.StatusCode)
	}

	return &Plugin{
		DB:                 db,
		Dock:               dock,
		Name:               cfg.PluginName,
		Listen:             cfg.Listen,
		Ver:                cfg.BuildVersion,
		MetricsTok:         cfg.MetricsToken,
		PublicURL:          strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/"),
		polarCredentialKey: credentialKey,
		metrics:            newDNSMetrics(),
		startedAt:          time.Now(),
	}, nil
}

// readJSON — minimal JSON unmarshaller (the SDK's is unexported).
func readJSON(resp *http.Response, out any) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func (p *Plugin) RegisterRoutes(r gin.IRouter) {
	r.GET("/healthz", p.handleHealthz)
	r.GET("/metrics", p.handleMetricsExposition)

	// /api/dns/* — user API. nginx proxies /api/dns/* → dns-svc. Record
	// write-through (create/update/delete) + audit reads land in M3; the
	// independent web/ UI in M5.
	api := r.Group("/api/dns")
	{
		auth := api.Group("", p.requireAuthViaDock())
		{
			auth.GET("/_whoami", p.handleWhoami)

			// Provider accounts.
			auth.POST("/providers", p.handleProviderCreate)
			auth.GET("/providers", p.handleProviderList)
			auth.PATCH("/providers/:id", p.handleProviderUpdate)
			auth.DELETE("/providers/:id", p.handleProviderDelete)
			auth.POST("/providers/:id/sync", p.handleProviderSync)

			// Zone/record cache (read).
			auth.GET("/zones", p.handleZonesList)
			auth.GET("/zones/:id/records", p.handleZoneRecords)

			// Declare a zone under a local (self-hosted) provider.
			auth.POST("/zones", p.handleZoneCreate)

			// Record write-through (provider-first, then cache + audit).
			auth.POST("/zones/:id/records", p.handleRecordCreate)
			auth.PATCH("/records/:id", p.handleRecordUpdate)
			auth.DELETE("/records/:id", p.handleRecordDelete)

			// DNS-as-Code: declarative apply (diff + write-through).
			auth.POST("/zones/:id/apply", p.handleZoneApply)

			// Audit log.
			auth.GET("/audit", p.handleAuditList)
		}
	}

	// Serve the embedded product UI for non-API paths (NoRoute is on the
	// engine, so this only wires up when given the *gin.Engine).
	if eng, ok := r.(*gin.Engine); ok {
		p.registerWeb(eng)
	}
}

func (p *Plugin) Start(ctx context.Context) {
	go p.heartbeatLoop(ctx)
}

func (p *Plugin) Close() error {
	if p.DB != nil {
		return p.DB.Close()
	}
	return nil
}

func (p *Plugin) handleHealthz(c *gin.Context) {
	dbOK := true
	if err := p.DB.PingContext(c.Request.Context()); err != nil {
		dbOK = false
	}
	status := http.StatusOK
	if !dbOK {
		status = http.StatusServiceUnavailable
	}
	c.JSON(status, gin.H{
		"plugin":            p.Name,
		"version":           p.Ver,
		"uptime_seconds":    int64(time.Since(p.startedAt).Seconds()),
		"db_ok":             dbOK,
		"encryption_active": len(p.polarCredentialKey) == dnsCredKeyBytes,
		"go":                runtime.Version(),
	})
}

func (p *Plugin) handleMetricsExposition(c *gin.Context) {
	if p.MetricsTok == "" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if c.GetHeader("Authorization") != "Bearer "+p.MetricsTok {
		c.Header("WWW-Authenticate", `Bearer realm="metrics"`)
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}
	promhttp.HandlerFor(p.metrics.registry, promhttp.HandlerOpts{}).ServeHTTP(c.Writer, c.Request)
}

// handleWhoami echoes the resolved identity — M0 probe that the
// AuthVerify + workspace-access middleware chain works end to end.
func (p *Plugin) handleWhoami(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"user_id":      c.GetString(ctxKeyUserID),
		"user_role":    c.GetString(ctxKeyUserRole),
		"workspace_id": c.GetString(ctxKeyWorkspaceID),
	})
}

func (p *Plugin) heartbeatLoop(ctx context.Context) {
	p.beat(ctx)
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.beat(ctx)
		}
	}
}

// dnsUIRoutes — sidebar entry this plugin contributes. Path is the root
// of the module's own UI (served by dns-svc); dock joins it with
// PublicBaseURL to build the cross-subdomain sidebar link.
var dnsUIRoutes = []sdk.UIRoute{
	{Path: "/", Label: "DNS", Icon: "globe", Order: 50},
}

func (p *Plugin) beat(_ context.Context) {
	err := p.Dock.Heartbeat(sdk.HeartbeatOpts{
		Version:       p.Ver,
		Endpoint:      p.Listen,
		UptimeSeconds: int64(time.Since(p.startedAt).Seconds()),
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		UIRoutes:      dnsUIRoutes,
		PublicBaseURL: p.PublicURL,
	})
	if err != nil {
		log.Printf("dns: heartbeat failed: %v", err)
	}
}
