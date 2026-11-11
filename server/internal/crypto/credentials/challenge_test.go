package credentials_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

// TestSealedChallenge: only the holder of the key opens a challenge, and only
// its value answers it (spec 2026-11-11-02).
func TestSealedChallenge(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	identity, recipient, err := credentials.GenerateX25519Keypair()
	r.NoError(err)
	other, _, err := credentials.GenerateX25519Keypair()
	r.NoError(err)

	sealed, sum, err := credentials.NewSealedChallenge(recipient)
	r.NoError(err)
	r.NotContains(sealed, sum)

	_, err = credentials.OpenSealedChallenge(sealed, other)
	r.Error(err, "another key cannot open it")

	value, err := credentials.OpenSealedChallenge(sealed, other, identity)
	r.NoError(err)
	r.True(credentials.ChallengeAnswered(value, sum))
	r.False(credentials.ChallengeAnswered(value+"0", sum))
	r.False(credentials.ChallengeAnswered("", sum))
	r.False(credentials.ChallengeAnswered(value, ""))

	again, againSum, err := credentials.NewSealedChallenge(recipient)
	r.NoError(err)
	r.NotEqual(sum, againSum, "every challenge is fresh")
	r.NotEqual(sealed, again)
}

// TestUnconfirmedBlobNeedsReseal: a re-wrap no second agent confirmed stays
// flagged even when sealed to the current recipients; a new re-wrap of it
// starts confirmed.
func TestUnconfirmedBlobNeedsReseal(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, recipient, err := credentials.GenerateX25519Keypair()
	r.NoError(err)

	fps, ct, err := credentials.SealForClient([]string{recipient}, map[string]any{"password": "pw"})
	r.NoError(err)
	envelope, err := credentials.BuildClientSealedEnvelope(fps, ct)
	r.NoError(err)

	need, err := credentials.NeedsReseal(envelope, fps)
	r.NoError(err)
	r.False(need)

	marked, err := credentials.MarkSealedUnconfirmed(envelope)
	r.NoError(err)
	r.True(credentials.IsSealedUnconfirmed(marked))
	r.Equal(credentials.SealOriginBrowser, credentials.SealedOrigin(marked))

	need, err = credentials.NeedsReseal(marked, fps)
	r.NoError(err)
	r.True(need)

	rewrapped, err := credentials.RewrapSealedEnvelope(marked, fps, ct)
	r.NoError(err)
	r.False(credentials.IsSealedUnconfirmed(rewrapped))
}
