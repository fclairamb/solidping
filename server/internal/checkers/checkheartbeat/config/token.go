package config

import (
	"crypto/rand"
	"encoding/hex"
)

const tokenLength = 16 // 16 bytes = 32 hex characters

// GenerateToken generates a random hex token. Exported so callers outside
// this package (the checks service's rotate-token endpoint) can mint a fresh
// token without duplicating the generation logic.
func GenerateToken() (string, error) {
	b := make([]byte, tokenLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return hex.EncodeToString(b), nil
}
