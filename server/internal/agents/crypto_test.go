package agents_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/agents"
)

func TestEnrollmentTokenMintAndHash(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	token, hash, err := agents.GenerateEnrollmentToken()
	r.NoError(err)
	r.True(strings.HasPrefix(token, agents.EnrollmentTokenPrefix))
	r.NotEqual(token, hash)
	// Hash is deterministic and matches HashEnrollmentToken.
	r.Equal(hash, agents.HashEnrollmentToken(token))
	// Two mints differ.
	token2, hash2, err := agents.GenerateEnrollmentToken()
	r.NoError(err)
	r.NotEqual(token, token2)
	r.NotEqual(hash, hash2)
}

func TestSignatureRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	keys, err := agents.GenerateAgentKeys()
	r.NoError(err)

	ts := time.Now().UTC().Format(time.RFC3339)
	sig, err := keys.Sign("GET", "/api/v1/agent/ws", ts, "nonce-1")
	r.NoError(err)

	r.NoError(agents.VerifySignature(keys.Ed25519PublicKey, "GET", "/api/v1/agent/ws", ts, "nonce-1", sig))
}

func TestSignatureRejectsTamper(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	keys, err := agents.GenerateAgentKeys()
	r.NoError(err)

	ts := time.Now().UTC().Format(time.RFC3339)
	sig, err := keys.Sign("GET", "/api/v1/agent/ws", ts, "nonce-1")
	r.NoError(err)

	// Different path -> fail.
	r.ErrorIs(
		agents.VerifySignature(keys.Ed25519PublicKey, "GET", "/api/v1/agent/other", ts, "nonce-1", sig),
		agents.ErrBadSignature,
	)
	// Different nonce -> fail.
	r.ErrorIs(
		agents.VerifySignature(keys.Ed25519PublicKey, "GET", "/api/v1/agent/ws", ts, "nonce-2", sig),
		agents.ErrBadSignature,
	)

	// Different key -> fail.
	other, err := agents.GenerateAgentKeys()
	r.NoError(err)
	r.ErrorIs(
		agents.VerifySignature(other.Ed25519PublicKey, "GET", "/api/v1/agent/ws", ts, "nonce-1", sig),
		agents.ErrBadSignature,
	)
}

func TestValidateTimestampSkew(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	// Within skew.
	r.NoError(agents.ValidateTimestamp(now.Add(-2*time.Minute).Format(time.RFC3339), now, agents.DefaultClockSkew))
	r.NoError(agents.ValidateTimestamp(now.Add(2*time.Minute).Format(time.RFC3339), now, agents.DefaultClockSkew))

	// Outside skew (past and future).
	r.ErrorIs(
		agents.ValidateTimestamp(now.Add(-10*time.Minute).Format(time.RFC3339), now, agents.DefaultClockSkew),
		agents.ErrClockSkew,
	)
	r.ErrorIs(
		agents.ValidateTimestamp(now.Add(10*time.Minute).Format(time.RFC3339), now, agents.DefaultClockSkew),
		agents.ErrClockSkew,
	)

	// Garbage timestamp.
	r.ErrorIs(agents.ValidateTimestamp("not-a-time", now, agents.DefaultClockSkew), agents.ErrInvalidTimestamp)
}

func TestKeyFingerprintStable(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	keys, err := agents.GenerateAgentKeys()
	r.NoError(err)

	fp1 := agents.KeyFingerprint(keys.Ed25519PublicKey)
	fp2 := agents.KeyFingerprint(keys.Ed25519PublicKey)
	r.Equal(fp1, fp2)
	r.Len(fp1, 16) // 8 bytes hex
}
