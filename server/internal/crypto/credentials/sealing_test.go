package credentials_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

// newAgentKeypair returns a fresh (identity, recipient) age keypair for one
// simulated agent.
func newAgentKeypair(t *testing.T) (string, string) {
	t.Helper()

	id, rec, err := credentials.GenerateX25519Keypair()
	require.NoError(t, err)
	require.NotEmpty(t, id)
	require.NotEmpty(t, rec)

	return id, rec
}

func TestSealUnsealRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	id, rec := newAgentKeypair(t)

	secrets := map[string]any{"password": "hunter2", "token": "abc"}
	env, err := credentials.SealForRecipients([]string{rec}, secrets)
	r.NoError(err)
	r.True(credentials.IsSealedEnvelope(env))

	got, err := credentials.UnsealWithIdentity(id, env)
	r.NoError(err)
	r.Equal("hunter2", got["password"])
	r.Equal("abc", got["token"])
}

// TestSealMultiRecipient asserts every recipient (HA agents in a region) can
// independently unseal the same blob.
func TestSealMultiRecipient(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	id1, rec1 := newAgentKeypair(t)
	id2, rec2 := newAgentKeypair(t)
	id3, rec3 := newAgentKeypair(t)

	env, err := credentials.SealForRecipients([]string{rec1, rec2, rec3}, map[string]any{"k": "v"})
	r.NoError(err)

	for _, id := range []string{id1, id2, id3} {
		got, err := credentials.UnsealWithIdentity(id, env)
		r.NoError(err)
		r.Equal("v", got["k"])
	}
}

func TestSealRequiresRecipients(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, err := credentials.SealForRecipients(nil, map[string]any{"k": "v"})
	r.ErrorIs(err, credentials.ErrNoRecipients)
}

// TestUnsealWrongIdentityFails asserts an agent not among the recipients cannot
// decrypt (the "needs re-seal" / decrypt-failure job error path).
func TestUnsealWrongIdentityFails(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, rec := newAgentKeypair(t)
	otherID, _ := newAgentKeypair(t)

	env, err := credentials.SealForRecipients([]string{rec}, map[string]any{"k": "v"})
	r.NoError(err)

	_, err = credentials.UnsealWithIdentity(otherID, env)
	r.Error(err)
}

// TestSealedEnvelopeDistinctFromV1 asserts the sealed blob is recognizably
// distinct from the v1 symmetric envelope and that the symmetric decrypt path
// rejects it (the server cannot recover a sealed-only credential).
func TestSealedEnvelopeDistinctFromV1(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, rec := newAgentKeypair(t)

	env, err := credentials.SealForRecipients([]string{rec}, map[string]any{"k": "v"})
	r.NoError(err)

	// It is a sealed envelope, not a v1 symmetric one.
	r.True(credentials.IsSealedEnvelope(env))

	// A v1 symmetric envelope is not a sealed one.
	svc, err := credentials.NewService(newKey(t), newFakeStore())
	r.NoError(err)
	v1, err := svc.EncryptForOrg(t.Context(), "org-1", map[string]any{"k": "v"})
	r.NoError(err)
	r.False(credentials.IsSealedEnvelope(v1))

	// The symmetric decrypt path cannot open a sealed blob (unknown version).
	_, err = svc.DecryptForOrg(t.Context(), "org-1", env)
	r.Error(err)
}

func TestNeedsReseal(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, recA := newAgentKeypair(t)
	_, recB := newAgentKeypair(t)

	env, err := credentials.SealForRecipients([]string{recA}, map[string]any{"k": "v"})
	r.NoError(err)

	fpA := credentials.RecipientFingerprint(recA)
	fpB := credentials.RecipientFingerprint(recB)

	// Blob was sealed to A only.
	sealed, err := credentials.SealedRecipientFingerprints(env)
	r.NoError(err)
	r.Equal([]string{fpA}, sealed)

	// Active set == {A}: no re-seal.
	need, err := credentials.NeedsReseal(env, []string{fpA})
	r.NoError(err)
	r.False(need)

	// A new agent B joined: needs re-seal (B cannot decrypt the old blob).
	need, err = credentials.NeedsReseal(env, []string{fpA, fpB})
	r.NoError(err)
	r.True(need)

	// A was revoked (active set now {B} or empty): needs re-seal — the blob is
	// still decryptable by the revoked agent and unreadable by the active one.
	need, err = credentials.NeedsReseal(env, []string{fpB})
	r.NoError(err)
	r.True(need)

	need, err = credentials.NeedsReseal(env, nil)
	r.NoError(err)
	r.True(need)
}

func TestSealedCiphertextAndClientSeal(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	id, rec := newAgentKeypair(t)

	fps, ct, err := credentials.SealForClient([]string{rec}, map[string]any{"password": "hunter2"})
	r.NoError(err)
	r.Equal([]string{credentials.RecipientFingerprint(rec)}, fps)
	r.NoError(credentials.ValidateSealedCiphertext(ct, 1))

	env, err := credentials.SealForRecipients([]string{rec}, map[string]any{"password": "x"})
	r.NoError(err)

	envCT, envFps, err := credentials.SealedCiphertext(env)
	r.NoError(err)
	r.Equal(fps, envFps)
	r.NotEmpty(envCT)

	// A client seal opens with the matching identity once wrapped in an envelope.
	rewrapped, err := credentials.RewrapSealedEnvelope(env, fps, ct)
	r.NoError(err)

	got, err := credentials.UnsealWithIdentity(id, rewrapped)
	r.NoError(err)
	r.Equal("hunter2", got["password"])
}

func TestSealedCiphertextRejectsBadInput(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, _, err := credentials.SealedCiphertext("not an envelope")
	r.Error(err)

	_, _, err = credentials.SealForClient(nil, map[string]any{"a": "b"})
	r.Error(err)

	_, err = credentials.RewrapSealedEnvelope("not an envelope", nil, "ct")
	r.Error(err)
}

func TestRewrapSealedEnvelopeSortsFingerprints(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	_, rec := newAgentKeypair(t)

	env, err := credentials.SealForRecipients([]string{rec}, map[string]any{"a": "b"})
	r.NoError(err)

	out, err := credentials.RewrapSealedEnvelope(env, []string{"zzz", "aaa"}, "new-ct")
	r.NoError(err)

	ct, fps, err := credentials.SealedCiphertext(out)
	r.NoError(err)
	r.Equal("new-ct", ct)
	r.Equal([]string{"aaa", "zzz"}, fps)
}
