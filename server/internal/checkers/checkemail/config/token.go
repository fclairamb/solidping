package config

import (
	"crypto/rand"
	"encoding/hex"
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
