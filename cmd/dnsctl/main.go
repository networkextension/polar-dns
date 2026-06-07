// Command dnsctl applies a declarative DNS zone file (DNS-as-Code, M6),
// kubectl-style: `dnsctl apply -f zone.yaml`. It resolves the zone name to
// its id via the dns API and POSTs the desired records to the apply
// endpoint (which diffs + write-throughs to the provider).
//
// Config (flags override env):
//
//	-api        DNS_API        e.g. https://dns.dev.4950.store
//	-token      DNS_TOKEN      dock session access token (Bearer)
//	-workspace  DNS_WORKSPACE  X-Workspace-Id
//	-f          zone file (yaml)
//	-dry-run    print the plan, change nothing
//	-prune      delete records not present in the file (skips SOA/NS)
//	-k          skip TLS verify (dev self-signed certs)
package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type recordInput struct {
	Type     string   `json:"type" yaml:"type"`
	Name     string   `json:"name,omitempty" yaml:"name"`
	Names    []string `json:"names,omitempty" yaml:"names"` // bulk fan-out
	Content  string   `json:"content" yaml:"content"`
	TTL      int      `json:"ttl,omitempty" yaml:"ttl"`
	Priority *int     `json:"priority,omitempty" yaml:"priority"`
	Proxied  bool     `json:"proxied,omitempty" yaml:"proxied"`
}

type zoneFile struct {
	Zone    string        `yaml:"zone"`
	Records []recordInput `yaml:"records"`
}

func main() {
	api := flag.String("api", env("DNS_API", "https://dns.dev.4950.store"), "dns API base URL")
	token := flag.String("token", os.Getenv("DNS_TOKEN"), "dock session access token (Bearer)")
	workspace := flag.String("workspace", os.Getenv("DNS_WORKSPACE"), "X-Workspace-Id")
	file := flag.String("f", "", "zone file (yaml)")
	dryRun := flag.Bool("dry-run", false, "print the plan without applying")
	prune := flag.Bool("prune", false, "delete records not in the file (skips SOA/NS)")
	insecure := flag.Bool("k", false, "skip TLS verification (dev self-signed)")
	flag.Parse()

	cmd := flag.Arg(0)
	if cmd != "apply" {
		fmt.Fprintln(os.Stderr, "usage: dnsctl apply -f zone.yaml [-dry-run] [-prune]")
		os.Exit(2)
	}
	if *file == "" || *token == "" {
		fail("required: -f <zone.yaml> and -token (or DNS_TOKEN)")
	}

	raw, err := os.ReadFile(*file)
	if err != nil {
		fail("read %s: %v", *file, err)
	}
	var zf zoneFile
	if err := yaml.Unmarshal(raw, &zf); err != nil {
		fail("parse %s: %v", *file, err)
	}
	if strings.TrimSpace(zf.Zone) == "" {
		fail("zone file must set `zone:`")
	}

	cl := &client{base: strings.TrimRight(*api, "/"), token: *token, workspace: *workspace,
		http: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: *insecure}, //nolint:gosec — opt-in dev flag
		}}}

	zoneID, err := cl.resolveZoneID(zf.Zone)
	if err != nil {
		fail("resolve zone %q: %v", zf.Zone, err)
	}

	body := map[string]any{"dry_run": *dryRun, "prune": *prune, "records": zf.Records}
	var out struct {
		DryRun  bool           `json:"dry_run"`
		Summary map[string]int `json:"summary"`
		Applied map[string]any `json:"applied"`
		Errors  []string       `json:"errors"`
		Plan    struct {
			Create []recordInput `json:"create"`
			Update []recordInput `json:"update"`
			Delete []recordInput `json:"delete"`
		} `json:"plan"`
	}
	if err := cl.postJSON("/api/dns/zones/"+zoneID+"/apply", body, &out); err != nil {
		fail("apply: %v", err)
	}

	if *dryRun {
		fmt.Printf("plan for %s (dry-run): +%d create  ~%d update  -%d delete\n",
			zf.Zone, out.Summary["create"], out.Summary["update"], out.Summary["delete"])
		printList("create", out.Plan.Create)
		printList("update", out.Plan.Update)
		printList("delete", out.Plan.Delete)
		return
	}
	fmt.Printf("applied to %s: created=%v updated=%v deleted=%v\n",
		zf.Zone, out.Applied["created"], out.Applied["updated"], out.Applied["deleted"])
	for _, e := range out.Errors {
		fmt.Fprintln(os.Stderr, "  error: "+e)
	}
	if len(out.Errors) > 0 {
		os.Exit(1)
	}
}

func printList(label string, recs []recordInput) {
	for _, r := range recs {
		name := r.Name
		if name == "" {
			name = "@"
		}
		fmt.Printf("  %-7s %-4s %-20s %s\n", label, r.Type, name, r.Content)
	}
}

type client struct {
	base, token, workspace string
	http                   *http.Client
}

func (c *client) do(method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if c.workspace != "" {
		req.Header.Set("X-Workspace-Id", c.workspace)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

func (c *client) resolveZoneID(zoneName string) (string, error) {
	resp, err := c.do(http.MethodGet, "/api/dns/zones", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Zones []struct {
			ID       string `json:"id"`
			ZoneName string `json:"zone_name"`
		} `json:"zones"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	for _, z := range out.Zones {
		if strings.EqualFold(z.ZoneName, zoneName) {
			return z.ID, nil
		}
	}
	return "", fmt.Errorf("zone not found in workspace (sync the provider first?)")
}

func (c *client) postJSON(path string, body, out any) error {
	b, _ := json.Marshal(body)
	resp, err := c.do(http.MethodPost, path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "dnsctl: "+format+"\n", a...)
	os.Exit(1)
}
