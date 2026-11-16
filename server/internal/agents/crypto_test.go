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

// TestSignatureV2RoundTrip: the v2 challenge verifies when method, full
// request URI, timestamp, nonce and body all match what was signed.
func TestSignatureV2RoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	keys, err := agents.GenerateAgentKeys()
	r.NoError(err)

	ts := time.Now().UTC().Format(time.RFC3339)
	body := []byte(`{"result":"ok"}`)
	uri := "/api/v1/agent/attachments?topic=screenshot"

	sig, err := keys.SignV2("POST", uri, ts, "nonce-1", body)
	r.NoError(err)

	r.NoError(agents.VerifySignatureV2(keys.Ed25519PublicKey, "POST", uri, ts, "nonce-1", body, sig))

	// A bodyless call (the WS reconnect) hashes the empty slice.
	sigEmpty, err := keys.SignV2("GET", "/api/v1/agent/ws", ts, "nonce-2", nil)
	r.NoError(err)
	r.NoError(agents.VerifySignatureV2(keys.Ed25519PublicKey, "GET", "/api/v1/agent/ws", ts, "nonce-2", nil, sigEmpty))
}

// TestSignatureV2RejectsTamper is the point of the v2 scheme: every component
// an on-path attacker could rewrite on a plain-HTTP agent link — the query,
// the body, the path — is bound into the signature.
func TestSignatureV2RejectsTamper(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	keys, err := agents.GenerateAgentKeys()
	r.NoError(err)

	ts := time.Now().UTC().Format(time.RFC3339)
	body := []byte(`{"result":"ok"}`)
	uri := "/api/v1/agent/attachments?topic=screenshot"

	sig, err := keys.SignV2("POST", uri, ts, "nonce-1", body)
	r.NoError(err)

	// Rewritten body -> fail.
	r.ErrorIs(agents.VerifySignatureV2(
		keys.Ed25519PublicKey, "POST", uri, ts, "nonce-1", []byte(`{"result":"forged"}`), sig),
		agents.ErrBadSignature)

	// Rewritten query (same path) -> fail.
	r.ErrorIs(agents.VerifySignatureV2(
		keys.Ed25519PublicKey, "POST", "/api/v1/agent/attachments?topic=trace", ts, "nonce-1", body, sig),
		agents.ErrBadSignature)

	// Dropped query -> fail.
	r.ErrorIs(agents.VerifySignatureV2(
		keys.Ed25519PublicKey, "POST", "/api/v1/agent/attachments", ts, "nonce-1", body, sig),
		agents.ErrBadSignature)

	// Different path / nonce / method -> fail.
	r.ErrorIs(agents.VerifySignatureV2(
		keys.Ed25519PublicKey, "POST", "/api/v1/agent/tasks/claim", ts, "nonce-1", body, sig),
		agents.ErrBadSignature)
	r.ErrorIs(agents.VerifySignatureV2(
		keys.Ed25519PublicKey, "POST", uri, ts, "nonce-2", body, sig),
		agents.ErrBadSignature)
	r.ErrorIs(agents.VerifySignatureV2(
		keys.Ed25519PublicKey, "GET", uri, ts, "nonce-1", body, sig),
		agents.ErrBadSignature)

	// Different key -> fail.
	other, err := agents.GenerateAgentKeys()
	r.NoError(err)
	r.ErrorIs(agents.VerifySignatureV2(other.Ed25519PublicKey, "POST", uri, ts, "nonce-1", body, sig),
		agents.ErrBadSignature)
}

// TestSignatureVersionsDoNotCrossVerify: a v1 signature must not verify as v2
// (or the reverse) — the version prefix makes the two canonical messages
// unambiguous, so an attacker cannot downgrade a captured v2 signature to the
// weaker path-only challenge.
func TestSignatureVersionsDoNotCrossVerify(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	keys, err := agents.GenerateAgentKeys()
	r.NoError(err)

	ts := time.Now().UTC().Format(time.RFC3339)
	uri := "/api/v1/agent/attachments?topic=screenshot"
	body := []byte("capture-bytes")

	v1, err := keys.Sign("POST", "/api/v1/agent/attachments", ts, "nonce-1")
	r.NoError(err)
	v2, err := keys.SignV2("POST", uri, ts, "nonce-1", body)
	r.NoError(err)

	// v1 signature presented to the v2 verifier -> fail.
	r.ErrorIs(agents.VerifySignatureV2(keys.Ed25519PublicKey, "POST", uri, ts, "nonce-1", body, v1),
		agents.ErrBadSignature)
	// v2 signature presented to the v1 verifier -> fail.
	r.ErrorIs(agents.VerifySignature(
		keys.Ed25519PublicKey, "POST", "/api/v1/agent/attachments", ts, "nonce-1", v2),
		agents.ErrBadSignature)
}
