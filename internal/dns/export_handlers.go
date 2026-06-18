package dns

// export_handlers.go — GET /api/dns/export. A read-only snapshot of the
// 'local' (self-hosted) zones for the split-horizon resolver agent
// (polar-dns-resolver) to render into BIND/Unbound. See
// modules/polar-dns-resolver/doc/sync-design.md §1.2 + §5.
//
//   GET /api/dns/export                  full snapshot, all views
//   GET /api/dns/export?view=private,any only those views' records
//   GET /api/dns/export?meta=1           lightweight {zone, serial} only
//
// Change detection: the response carries an ETag derived from every zone's
// (name, serial). The agent polls with If-None-Match and gets 304 when
// nothing changed, then pulls the full body only on a miss. SOA/NS/glue are
// NOT part of this payload — they live in the resolver's own config (zone
// metadata is the resolver's concern; polar-dns owns the records).

import (
	"fmt"
	"hash/fnv"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

type exportRecord struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Content  string `json:"content"`
	TTL      int    `json:"ttl"`
	Priority *int   `json:"priority,omitempty"`
	View     string `json:"view"`
}

type exportZone struct {
	Zone    string         `json:"zone"`
	Serial  int64          `json:"serial"`
	Records []exportRecord `json:"records,omitempty"` // omitted in meta mode
}

type exportResp struct {
	GeneratedAt time.Time    `json:"generated_at"`
	Zones       []exportZone `json:"zones"`
}

// parseViewFilter turns "private,any" into a set. Returns nil for an empty
// filter, meaning "all views". Unknown view tokens are ignored.
func parseViewFilter(s string) map[string]bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	out := map[string]bool{}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue // validateView("") would map to "any"; skip stray empties
		}
		if v, ok := validateView(part); ok {
			out[v] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// exportETag is a weak version tag over every zone's (name, serial). Stable
// across requests (listLocalZones is name-ordered); changes iff some zone's
// serial advances — i.e. iff a record was written.
func exportETag(zones []ZoneRow) string {
	h := fnv.New64a()
	for _, z := range zones {
		fmt.Fprintf(h, "%s:%d;", z.ZoneName, z.Serial)
	}
	return fmt.Sprintf(`"%x"`, h.Sum64())
}

func (p *Plugin) handleExport(c *gin.Context) {
	ctx := c.Request.Context()
	wsID := c.GetString(ctxKeyWorkspaceID)
	meta := c.Query("meta") == "1"
	filter := parseViewFilter(c.Query("view"))

	zones, err := p.listLocalZones(ctx, wsID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	etag := exportETag(zones)
	c.Header("ETag", etag)
	c.Header("Cache-Control", "no-cache")
	if match := strings.TrimSpace(c.GetHeader("If-None-Match")); match == etag {
		c.Status(http.StatusNotModified)
		return
	}

	resp := exportResp{GeneratedAt: time.Now().UTC(), Zones: make([]exportZone, 0, len(zones))}
	for _, z := range zones {
		ez := exportZone{Zone: z.ZoneName, Serial: z.Serial}
		if !meta {
			recs, err := p.listRecordsByZone(ctx, wsID, z.ID)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			for _, r := range recs {
				if filter != nil && !filter[r.View] {
					continue
				}
				ez.Records = append(ez.Records, exportRecord{
					Name: r.Name, Type: r.Type, Content: r.Content, TTL: r.TTL, Priority: r.Priority, View: r.View,
				})
			}
		}
		resp.Zones = append(resp.Zones, ez)
	}
	c.JSON(http.StatusOK, resp)
}
