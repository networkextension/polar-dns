package dns

import "testing"

func strptr(s string) *string { return &s }
func intptr(i int) *int       { return &i }
func boolptr(b bool) *bool    { return &b }

func TestApplyRecordPatch_NoOpWhenAllNil(t *testing.T) {
	pr := 10
	cur := Record{RemoteID: "5", Type: "A", Name: "www", Content: "1.1.1.1", TTL: 300, Priority: &pr, Proxied: true}
	got := applyRecordPatch(cur, updateRecordReq{})
	if got != cur {
		t.Fatalf("empty patch should be a no-op: got %+v want %+v", got, cur)
	}
}

func TestApplyRecordPatch_OverlaysProvidedFields(t *testing.T) {
	cur := Record{RemoteID: "5", Type: "A", Name: "www", Content: "1.1.1.1", TTL: 300}
	got := applyRecordPatch(cur, updateRecordReq{
		Content: strptr("2.2.2.2"),
		TTL:     intptr(60),
	})
	if got.Content != "2.2.2.2" || got.TTL != 60 {
		t.Fatalf("provided fields not applied: %+v", got)
	}
	// untouched fields stay
	if got.Type != "A" || got.Name != "www" || got.RemoteID != "5" {
		t.Fatalf("untouched fields changed: %+v", got)
	}
}

func TestApplyRecordPatch_PriorityAndProxied(t *testing.T) {
	cur := Record{Type: "MX", Content: "mail", TTL: 300}
	got := applyRecordPatch(cur, updateRecordReq{Priority: intptr(20), Proxied: boolptr(true)})
	if got.Priority == nil || *got.Priority != 20 {
		t.Fatalf("priority not applied: %+v", got)
	}
	if !got.Proxied {
		t.Fatalf("proxied not applied: %+v", got)
	}
}

func TestRecordRowToRecord(t *testing.T) {
	pr := 5
	row := RecordRow{RemoteRecordID: "9", Type: "MX", Name: "@", Content: "mail", TTL: 120, Priority: &pr, Proxied: false}
	r := recordRowToRecord(row)
	if r.RemoteID != "9" || r.Type != "MX" || r.Content != "mail" || r.TTL != 120 || r.Priority == nil || *r.Priority != 5 {
		t.Fatalf("row→record mapping wrong: %+v", r)
	}
}
