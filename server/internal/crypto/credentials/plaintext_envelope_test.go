package credentials_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

// TestPlaintextEnvelopeRoundTrip pins the seal/open round-trip: whatever secret
// map goes in comes back out byte-for-byte, no key involved.
func TestPlaintextEnvelopeRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	secrets := map[string]any{
		"private_key": "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END-----",
		"basicAuth":   "user:pass",
	}

	env, err := credentials.SealPlaintext(secrets)
	r.NoError(err)
	r.True(credentials.IsPlaintextEnvelope(env))

	out, err := credentials.OpenPlaintext(env)
	r.NoError(err)
	r.Equal(secrets, out)
}

// TestPlaintextEnvelopeDoesNotCollideWithOtherEnvelopes proves the v3 marker is
// distinguishable from the v1 (AES-GCM) and v2 (age-sealed) envelopes so the
// three can share the config_private column without ambiguity.
func TestPlaintextEnvelopeDoesNotCollideWithOtherEnvelopes(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// A v1 AES-GCM envelope.
	svc, err := credentials.NewService(make([]byte, 32), newFakeStore())
	r.NoError(err)
	aesEnv, err := svc.EncryptForOrg(t.Context(), "org", map[string]any{"password": "x"})
	r.NoError(err)

	r.False(credentials.IsPlaintextEnvelope(aesEnv), "an AES envelope must not read as plaintext")
	r.True(credentials.RequiresKey(aesEnv), "an AES envelope needs a key")

	// A v2 region-sealed envelope.
	_, recipient, err := credentials.GenerateX25519Keypair()
	r.NoError(err)
	sealedEnv, err := credentials.SealForRecipients([]string{recipient}, map[string]any{"password": "x"})
	r.NoError(err)

	r.False(credentials.IsPlaintextEnvelope(sealedEnv), "a sealed envelope must not read as plaintext")
	r.True(credentials.RequiresKey(sealedEnv), "a sealed envelope needs a key")

	// The plaintext envelope itself.
	plainEnv, err := credentials.SealPlaintext(map[string]any{"password": "x"})
	r.NoError(err)
	r.True(credentials.IsPlaintextEnvelope(plainEnv))
	r.False(credentials.RequiresKey(plainEnv), "a plaintext envelope needs no key")
	r.False(credentials.IsSealedEnvelope(plainEnv), "a plaintext envelope is not a sealed blob")
}

// TestOpenPlaintextRejectsOtherEnvelopes ensures OpenPlaintext refuses a
// non-plaintext blob rather than returning garbage.
func TestOpenPlaintextRejectsOtherEnvelopes(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, err := credentials.OpenPlaintext(`{"v":2,"alg":"age-x25519","ct":"zzz"}`)
	r.ErrorIs(err, credentials.ErrNotPlaintextEnvelope)

	r.False(credentials.IsPlaintextEnvelope("not json at all"))
	r.False(credentials.RequiresKey(""), "an empty envelope needs no key")
}

// TestDecryptForOrgOpensPlaintextOnDisabledService is the load-bearing property
// for the no-master-key path: a DISABLED credentials service must still
// reconstitute a plaintext envelope through the same DecryptForOrg seam every
// caller uses, so checks and notifications keep working with no key configured.
func TestDecryptForOrgOpensPlaintextOnDisabledService(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	disabled, err := credentials.NewService(nil, newFakeStore())
	r.NoError(err)
	r.False(disabled.Enabled())

	env, err := credentials.SealPlaintext(map[string]any{"private_key": "SEKRET"})
	r.NoError(err)

	out, err := disabled.DecryptForOrg(t.Context(), "org", env)
	r.NoError(err)
	r.Equal("SEKRET", out["private_key"])

	// A non-plaintext envelope on a disabled service still errors out.
	_, err = disabled.DecryptForOrg(t.Context(), "org", `{"v":1,"alg":"AES-256-GCM","nonce":"x","ct":"y"}`)
	r.ErrorIs(err, credentials.ErrDisabled)
}
