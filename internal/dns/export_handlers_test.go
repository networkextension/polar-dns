package dns

import "testing"

func TestParseViewFilter(t *testing.T) {
	cases := []struct {
		in   string
		want map[string]bool
	}{
		{"", nil},   // empty → all views
		{"  ", nil}, // blank → all views
		{"private", map[string]bool{"private": true}},
		{"private,any", map[string]bool{"private": true, "any": true}},
		{" private , Any ", map[string]bool{"private": true, "any": true}}, // trim + case
		{"public,private,any", map[string]bool{"public": true, "private": true, "any": true}},
		{"bogus", nil}, // unknown only → nil (all)
		{"private,bogus", map[string]bool{"private": true}}, // unknown dropped
		{"private,,", map[string]bool{"private": true}},     // stray empties skipped
	}
	for _, tc := range cases {
		got := parseViewFilter(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("parseViewFilter(%q)=%v want %v", tc.in, got, tc.want)
			continue
		}
		for k := range tc.want {
			if !got[k] {
				t.Errorf("parseViewFilter(%q)=%v missing %q", tc.in, got, k)
			}
		}
	}
}

func TestExportETag(t *testing.T) {
	a := []ZoneRow{{ZoneName: "4950.store", Serial: 3}, {ZoneName: "b.test", Serial: 1}}
	// Same data → same ETag (stable).
	if exportETag(a) != exportETag(a) {
		t.Fatal("ETag not stable for identical input")
	}
	// A serial bump changes the ETag.
	b := []ZoneRow{{ZoneName: "4950.store", Serial: 4}, {ZoneName: "b.test", Serial: 1}}
	if exportETag(a) == exportETag(b) {
		t.Fatal("ETag must change when a serial advances")
	}
	// A new zone changes the ETag.
	c := append([]ZoneRow{}, a...)
	c = append(c, ZoneRow{ZoneName: "c.test", Serial: 1})
	if exportETag(a) == exportETag(c) {
		t.Fatal("ETag must change when the zone set changes")
	}
	// Quoted (RFC 7232 entity-tag).
	if et := exportETag(a); et == "" || et[0] != '"' {
		t.Fatalf("ETag should be a quoted string, got %q", et)
	}
}
