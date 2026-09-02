package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// randomToken returns n bytes of cryptographic randomness in an
// unpadded-URL-safe alphabet, so a key survives being pasted into a shell, a
// URL, or a YAML file unquoted.
func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand cannot fail on any platform this runs on; if it somehow
		// does, minting a predictable credential would be far worse than
		// refusing to start.
		panic(fmt.Sprintf("cognigate: crypto/rand unavailable: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// NewID mints an opaque, prefixed identifier. The prefix makes an id
// self-describing in a log line or a support ticket.
func NewID(kind string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("cognigate: crypto/rand unavailable: %v", err))
	}
	return kind + "_" + hex.EncodeToString(b)
}

// Id prefixes for each resource kind.
const (
	IDTenant   = "ten"
	IDKey      = "key"
	IDProvider = "prv"
	IDAlias    = "als"
	IDRoute    = "rte"
	IDWebhook  = "whk"
	IDRequest  = "req"
	IDEvent    = "evt"
	IDAudit    = "aud"
)

// GenerateAPIKey mints a credential for one plane. `dev` inserts a visible
// marker so a key minted by the throwaway in-memory dev server can never be
// mistaken for one that guards a real deployment.
//
// It returns the plaintext (shown to the caller exactly once), the display
// prefix, and the hash the store keeps.
func GenerateAPIKey(plane Plane, dev bool) (plaintext, prefix, hash string) {
	scheme := DataKeyPrefix
	if plane == PlaneAdmin {
		scheme = AdminKeyPrefix
	}
	if dev {
		scheme += "dev-"
	}
	plaintext = scheme + randomToken(24)
	return plaintext, KeyPrefix(plaintext), HashAPIKey(plaintext)
}

// HashAPIKey is the one-way function guarding stored credentials. SHA-256 is
// the right primitive here rather than a password hash: these are 192-bit
// random tokens, not user-chosen secrets, so there is no dictionary to slow
// down — and the hash runs on every authenticated request.
func HashAPIKey(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// KeyPrefix is the non-secret leading fragment kept for display and for the
// key_prefix column on usage records. It is short enough to be useless as a
// credential and long enough to tell two of a tenant's keys apart.
func KeyPrefix(plaintext string) string {
	const shown = 8
	scheme := DataKeyPrefix
	if strings.HasPrefix(plaintext, AdminKeyPrefix) {
		scheme = AdminKeyPrefix
	}
	if strings.HasPrefix(plaintext[len(scheme):], "dev-") {
		scheme += "dev-"
	}
	rest := plaintext[len(scheme):]
	if len(rest) > shown {
		rest = rest[:shown]
	}
	return scheme + rest
}

// PlaneOf reads the plane from the credential itself, before any store lookup.
// That is what lets a cg- key presented to /admin/v1 be answered with
// wrong_plane rather than a generic rejection.
func PlaneOf(plaintext string) (Plane, bool) {
	switch {
	// Checked first: "cga-" also has the "cg" bigram, so the longer scheme has
	// to win or every admin key would read as a data key.
	case strings.HasPrefix(plaintext, AdminKeyPrefix):
		return PlaneAdmin, true
	case strings.HasPrefix(plaintext, DataKeyPrefix):
		return PlaneData, true
	default:
		return "", false
	}
}

// ConstantTimeEqual compares two hex digests without leaking, through timing,
// how many leading characters matched.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
