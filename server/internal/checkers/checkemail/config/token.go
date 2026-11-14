package config

import (
	"crypto/rand"
	"encoding/hex"
	"regexp"
)

// tokenByteLength is the number of random bytes (24 → 48 hex chars).
const tokenByteLength = 24

// generateToken returns a 48-character random hex string. 24 random bytes is
// long enough that we don't need org-scoping in the lookup — the token alone
// identifies the check globally with negligible collision risk.
func generateToken() (string, error) {
	b := make([]byte, tokenByteLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return hex.EncodeToString(b), nil
}

// tokenPattern is the only token shape the server mints, and the only one the
// inbound handler matches: 48 lowercase hex characters.
var tokenPattern = regexp.MustCompile(`^[0-9a-f]{48}$`)

// IsValidToken reports whether token has the shape the server mints.
func IsValidToken(token string) bool {
	return tokenPattern.MatchString(token)
}
