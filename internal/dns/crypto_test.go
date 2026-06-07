package dns

import (
	"crypto/rand"
	"io"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, dnsCredKeyBytes)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	key := testKey(t)
	plain := `{"username":"foo","token":"s3cr3t-token"}`

	ct, err := seal(key, plain)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if ct == plain || ct == "" {
		t.Fatalf("seal produced unexpected output %q", ct)
	}

	got, err := open(key, ct)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got != plain {
		t.Fatalf("round-trip mismatch: got %q want %q", got, plain)
	}
}

func TestSealNoncedNonDeterministic(t *testing.T) {
	key := testKey(t)
	a, _ := seal(key, "same")
	b, _ := seal(key, "same")
	if a == b {
		t.Fatal("seal must use a fresh nonce per call (got identical ciphertexts)")
	}
}

func TestOpenWrongKeyFails(t *testing.T) {
	ct, err := seal(testKey(t), "secret")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := open(testKey(t), ct); err == nil {
		t.Fatal("open with wrong key should fail")
	}
}

func TestNewGCMRejectsBadKeyLen(t *testing.T) {
	if _, err := seal([]byte("short"), "x"); err == nil {
		t.Fatal("seal should reject a short key")
	}
}
