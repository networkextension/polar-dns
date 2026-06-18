package dns

import (
	"context"
	"testing"
)

func TestLocalProviderRegistered(t *testing.T) {
	found := false
	for _, typ := range ListProviderTypes() {
		if typ == localType {
			found = true
		}
	}
	if !found {
		t.Fatalf("local not registered; got %v", ListProviderTypes())
	}
	// Factory must build with no credentials and no proxy.
	prov, err := NewProvider(localType, nil, "")
	if err != nil {
		t.Fatalf("NewProvider(local) errored: %v", err)
	}
	if prov.Type() != localType {
		t.Fatalf("Type()=%q want %q", prov.Type(), localType)
	}
	if prov.Capabilities().Proxied {
		t.Fatal("local provider should not advertise Proxied")
	}
}

func TestLocalProviderNoRemoteDiscovery(t *testing.T) {
	prov, _ := NewProvider(localType, nil, "")
	zones, err := prov.ListZones(context.Background())
	if err != nil || len(zones) != 0 {
		t.Fatalf("ListZones: want empty, got %v err=%v", zones, err)
	}
	recs, err := prov.ListRecords(context.Background(), "anything")
	if err != nil || len(recs) != 0 {
		t.Fatalf("ListRecords: want empty, got %v err=%v", recs, err)
	}
}

// CreateRecord must mint a unique, non-empty RemoteID — dns_record has
// UNIQUE(zone_id, remote_record_id), so two local records in a zone must not
// share an (empty) handle.
func TestLocalProviderCreateMintsUniqueRemoteID(t *testing.T) {
	prov, _ := NewProvider(localType, nil, "")
	a, err := prov.CreateRecord(context.Background(), "z", Record{Type: "A", Name: "x", Content: "1.1.1.1", TTL: 60})
	if err != nil {
		t.Fatalf("CreateRecord a: %v", err)
	}
	b, err := prov.CreateRecord(context.Background(), "z", Record{Type: "A", Name: "y", Content: "2.2.2.2", TTL: 60})
	if err != nil {
		t.Fatalf("CreateRecord b: %v", err)
	}
	if a.RemoteID == "" || b.RemoteID == "" {
		t.Fatalf("RemoteID must be non-empty: a=%q b=%q", a.RemoteID, b.RemoteID)
	}
	if a.RemoteID == b.RemoteID {
		t.Fatalf("RemoteID must be unique per record: %q", a.RemoteID)
	}
	// Content/Type are passed through unchanged.
	if a.Content != "1.1.1.1" || a.Type != "A" {
		t.Fatalf("create should pass fields through: %+v", a)
	}
}

func TestLocalProviderUpdateDeleteNoOp(t *testing.T) {
	prov, _ := NewProvider(localType, nil, "")
	in := Record{RemoteID: "loc_keep", Type: "A", Name: "x", Content: "3.3.3.3", TTL: 60}
	out, err := prov.UpdateRecord(context.Background(), "z", in)
	if err != nil || out.RemoteID != "loc_keep" || out.Content != "3.3.3.3" {
		t.Fatalf("UpdateRecord should return record unchanged: %+v err=%v", out, err)
	}
	if err := prov.DeleteRecord(context.Background(), "z", "loc_keep"); err != nil {
		t.Fatalf("DeleteRecord no-op should not error: %v", err)
	}
}
