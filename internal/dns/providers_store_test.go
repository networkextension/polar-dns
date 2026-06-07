package dns

import "testing"

func TestCredentialSealRoundTrip(t *testing.T) {
	p := &Plugin{polarCredentialKey: testKey(t)}
	cred := map[string]string{"username": "foo", "token": "s3cr3t"}

	cipher, plain, encrypted, err := p.sealCredential(cred)
	if err != nil {
		t.Fatalf("sealCredential: %v", err)
	}
	if !encrypted || cipher == "" || plain != "" {
		t.Fatalf("expected encrypted seal, got encrypted=%v cipher=%q plain=%q", encrypted, cipher, plain)
	}

	got, err := p.openCredential(ProviderAccount{Encrypted: true, credCipher: cipher})
	if err != nil {
		t.Fatalf("openCredential: %v", err)
	}
	if got["username"] != "foo" || got["token"] != "s3cr3t" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestCredentialPlaintextFallback(t *testing.T) {
	p := &Plugin{} // no key → degrade to plaintext
	cred := map[string]string{"username": "u", "token": "t"}

	cipher, plain, encrypted, err := p.sealCredential(cred)
	if err != nil {
		t.Fatalf("sealCredential: %v", err)
	}
	if encrypted || cipher != "" || plain == "" {
		t.Fatalf("expected plaintext fallback, got encrypted=%v cipher=%q plain=%q", encrypted, cipher, plain)
	}

	got, err := p.openCredential(ProviderAccount{Encrypted: false, credPlain: plain})
	if err != nil {
		t.Fatalf("openCredential: %v", err)
	}
	if got["token"] != "t" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestOpenEncryptedWithoutKeyErrors(t *testing.T) {
	p := &Plugin{} // no key
	if _, err := p.openCredential(ProviderAccount{Encrypted: true, credCipher: "deadbeef"}); err == nil {
		t.Fatal("openCredential should fail when row is encrypted but no key is configured")
	}
}

func TestBuildProviderFromAccount(t *testing.T) {
	p := &Plugin{polarCredentialKey: testKey(t)}
	cipher, _, _, err := p.sealCredential(map[string]string{"username": "u", "token": "t"})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	prov, err := p.buildProvider(ProviderAccount{
		ProviderType: namecomType, Encrypted: true, credCipher: cipher,
	})
	if err != nil {
		t.Fatalf("buildProvider: %v", err)
	}
	if prov.Type() != namecomType {
		t.Fatalf("built provider type=%q", prov.Type())
	}
}
