package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
)

func challengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))

	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestVerifyPKCE(t *testing.T) {
	t.Parallel()

	const verifier = "abcdefghijklmnopqrstuvwxyz1234567890ABCDEFG" // 43 chars
	good := challengeFor(verifier)

	tests := []struct {
		name      string
		verifier  string
		challenge string
		want      bool
	}{
		{"matching S256", verifier, good, true},
		{"wrong verifier", "not-the-right-verifier-value-padding-1234567", good, false},
		{"empty verifier", "", good, false},
		{"empty challenge", verifier, "", false},
		{"both empty", "", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, VerifyPKCE(tc.verifier, tc.challenge))
		})
	}
}

func TestValidCodeChallenge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		challenge string
		want      bool
	}{
		{"43-char base64url", challengeFor("verifier"), true},
		{"too short", "short", false},
		{"empty", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, validCodeChallenge(tc.challenge))
		})
	}
}
