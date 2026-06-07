package dns

// crypto.go — AES-256-GCM at-rest encryption for dns_provider credentials
// (provider API tokens). Mirrors the hosts plugin's credential sealing.
// The key is $DNS_CRED_KEY (32 bytes, hex-decoded in New). When the key is
// absent the caller stores plaintext and flips the row's encrypted flag to
// false — these helpers are only invoked once a valid key is present.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// seal encrypts plaintext with key (must be dnsCredKeyBytes long) and
// returns base64(nonce || ciphertext+tag).
func seal(key []byte, plaintext string) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("nonce: %w", err)
	}
	out := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(out), nil
}

// open reverses seal.
func open(key []byte, cipherB64 string) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil {
		return "", fmt.Errorf("base64: %w", err)
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := raw[:ns], raw[ns:]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("gcm open: %w", err)
	}
	return string(pt), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != dnsCredKeyBytes {
		return nil, fmt.Errorf("dns crypto: key length=%d want=%d", len(key), dnsCredKeyBytes)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
