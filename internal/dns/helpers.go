package dns

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"

	"github.com/lib/pq"
)

// newID mints a prefixed, random TEXT id (e.g. dp_<32hex>), matching the
// platform convention of domain-prefixed string ids.
func newID(prefix string) string {
	b := make([]byte, 16)
	_, _ = io.ReadFull(rand.Reader, b)
	return prefix + hex.EncodeToString(b)
}

// isUniqueViolation reports whether err is a Postgres unique_violation
// (SQLSTATE 23505) — used to translate duplicate inserts into HTTP 409.
func isUniqueViolation(err error) bool {
	var pe *pq.Error
	if errors.As(err, &pe) {
		return pe.Code == "23505"
	}
	return false
}

// nullJSON returns a value suitable for a JSONB column: the raw bytes as
// a string, or nil when empty (so the column stores SQL NULL).
func nullJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}
