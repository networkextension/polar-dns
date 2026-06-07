package dns

import (
	"testing"
	"time"
)

func TestNewProviderUnknownType(t *testing.T) {
	if _, err := NewProvider("does-not-exist", nil, ""); err == nil {
		t.Fatal("expected error for unknown provider type")
	}
}

func TestListProviderTypesIncludesNamecom(t *testing.T) {
	found := false
	for _, typ := range ListProviderTypes() {
		if typ == namecomType {
			found = true
		}
	}
	if !found {
		t.Fatalf("namecom not registered; got %v", ListProviderTypes())
	}
}

func TestNewHTTPClientProxyValidation(t *testing.T) {
	cases := []struct {
		proxy   string
		wantErr bool
	}{
		{"", false},
		{"http://127.0.0.1:7890", false},
		{"https://127.0.0.1:7890", false},
		{"socks5://127.0.0.1:1080", false},
		{"ftp://127.0.0.1:21", true},
		{"file:///etc/passwd", true},
		{"://bad", true},
	}
	for _, tc := range cases {
		_, err := newHTTPClient(tc.proxy, time.Second)
		if tc.wantErr && err == nil {
			t.Errorf("proxy %q: expected error, got nil", tc.proxy)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("proxy %q: unexpected error: %v", tc.proxy, err)
		}
	}
}

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{"@": "", " @ ": "", "www": "www", "": ""}
	for in, want := range cases {
		if got := normalizeHost(in); got != want {
			t.Errorf("normalizeHost(%q)=%q want %q", in, got, want)
		}
	}
}
