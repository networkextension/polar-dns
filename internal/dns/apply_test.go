package dns

import "testing"

func rec(typ, name, content string, ttl int) Record {
	return Record{Type: typ, Name: name, Content: content, TTL: ttl}
}

func TestDiff_CreateNew(t *testing.T) {
	cur := []Record{rec("A", "www", "1.1.1.1", 300)}
	des := []Record{rec("A", "www", "1.1.1.1", 300), rec("A", "blog", "2.2.2.2", 300)}
	p := diffRecords(cur, des, false)
	if len(p.Create) != 1 || p.Create[0].Name != "blog" {
		t.Fatalf("expected 1 create (blog), got %+v", p.Create)
	}
	if len(p.Update) != 0 || len(p.Delete) != 0 {
		t.Fatalf("unexpected update/delete: %+v", p)
	}
}

func TestDiff_UpdateTTL(t *testing.T) {
	cur := []Record{{RemoteID: "r1", Type: "A", Name: "www", Content: "1.1.1.1", TTL: 300}}
	des := []Record{rec("A", "www", "1.1.1.1", 60)}
	p := diffRecords(cur, des, false)
	if len(p.Update) != 1 || p.Update[0].RemoteID != "r1" || p.Update[0].TTL != 60 {
		t.Fatalf("expected ttl update carrying RemoteID r1, got %+v", p.Update)
	}
	if len(p.Create) != 0 || len(p.Delete) != 0 {
		t.Fatalf("unexpected create/delete: %+v", p)
	}
}

func TestDiff_DeleteExtraValueInDeclaredGroup(t *testing.T) {
	// www/A declared with only 1.1.1.1 → the extra 2.2.2.2 must be deleted
	cur := []Record{
		{RemoteID: "r1", Type: "A", Name: "www", Content: "1.1.1.1", TTL: 300},
		{RemoteID: "r2", Type: "A", Name: "www", Content: "2.2.2.2", TTL: 300},
	}
	des := []Record{rec("A", "www", "1.1.1.1", 300)}
	p := diffRecords(cur, des, false)
	if len(p.Delete) != 1 || p.Delete[0].RemoteID != "r2" {
		t.Fatalf("expected delete of r2, got %+v", p.Delete)
	}
	if len(p.Create) != 0 || len(p.Update) != 0 {
		t.Fatalf("unexpected create/update: %+v", p)
	}
}

func TestDiff_UnmentionedGroupUntouchedWithoutPrune(t *testing.T) {
	cur := []Record{{RemoteID: "m1", Type: "MX", Name: "", Content: "mail", TTL: 300}}
	des := []Record{rec("A", "www", "1.1.1.1", 300)}
	p := diffRecords(cur, des, false)
	if len(p.Delete) != 0 {
		t.Fatalf("MX group not mentioned must survive without prune, got delete %+v", p.Delete)
	}
	if len(p.Create) != 1 {
		t.Fatalf("expected www create, got %+v", p.Create)
	}
}

func TestDiff_PruneDeletesUnmentionedButSkipsSOANS(t *testing.T) {
	cur := []Record{
		{RemoteID: "m1", Type: "MX", Name: "", Content: "mail", TTL: 300},
		{RemoteID: "ns1", Type: "NS", Name: "", Content: "ns1.p.net", TTL: 300},
		{RemoteID: "soa", Type: "SOA", Name: "", Content: "soa", TTL: 300},
	}
	des := []Record{rec("A", "www", "1.1.1.1", 300)}
	p := diffRecords(cur, des, true)
	if len(p.Delete) != 1 || p.Delete[0].RemoteID != "m1" {
		t.Fatalf("prune should delete only the MX (skip SOA/NS), got %+v", p.Delete)
	}
}

func TestExpandApplyInputs_NamesFanOut(t *testing.T) {
	in := []applyRecordInput{
		{Type: "a", Content: "10.0.0.1", Names: []string{"a", "b", "c"}},
		{Type: "TXT", Name: "@", Content: "hi"},
	}
	out := expandApplyInputs(in)
	if len(out) != 4 {
		t.Fatalf("expected 4 records (3 fanned + 1), got %d: %+v", len(out), out)
	}
	got := map[string]bool{}
	for _, r := range out[:3] {
		if r.Type != "A" || r.Content != "10.0.0.1" || r.TTL != 300 {
			t.Fatalf("fanned record wrong: %+v", r)
		}
		got[r.Name] = true
	}
	for _, n := range []string{"a", "b", "c"} {
		if !got[n] {
			t.Fatalf("missing fanned name %q in %+v", n, out)
		}
	}
	if out[3].Name != "" { // apex "@" normalized
		t.Fatalf("apex not normalized: %q", out[3].Name)
	}
}

func TestExpandApplyInputs_SingleNoNames(t *testing.T) {
	out := expandApplyInputs([]applyRecordInput{{Type: "A", Name: "www", Content: "1.1.1.1"}})
	if len(out) != 1 || out[0].Name != "www" || out[0].TTL != 300 {
		t.Fatalf("single expand wrong: %+v", out)
	}
}

func TestDiff_ApexNameNormalization(t *testing.T) {
	// current apex stored as "", desired uses "@" — must match (no churn)
	cur := []Record{{RemoteID: "a1", Type: "A", Name: "", Content: "1.1.1.1", TTL: 300}}
	des := []Record{rec("A", "@", "1.1.1.1", 300)}
	p := diffRecords(cur, des, true)
	if !p.empty() {
		t.Fatalf("apex @ vs '' should be a no-op, got %+v", p)
	}
}
