package passwords

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

// authConfigWithAlgorithm builds a minimal AuthConfig carrying the given
// password algorithm, for exercising PolicyFromConfig.
func authConfigWithAlgorithm(algo string) config.AuthConfig {
	return config.AuthConfig{
		Password: config.PasswordConfig{
			Algorithm: algo,
			Argon2: config.Argon2Params{
				Memory: 64 * 1024, Time: 3, Threads: 4, KeyLength: 32, SaltLength: 16,
			},
			Bcrypt: config.BcryptParams{Cost: 12},
		},
	}
}

// argon2Default and bcryptPolicy are convenience policies for tests. They never
// mutate the package default unless explicitly installed via withPolicy.
func argon2DefaultPolicy() Policy {
	return Policy{
		Algorithm: argon2idID,
		Argon2:    defaultArgon2Params,
		Bcrypt:    BcryptParams{Cost: 12},
	}
}

func bcryptPolicy(cost int) Policy {
	return Policy{
		Algorithm: bcryptID,
		Argon2:    defaultArgon2Params,
		Bcrypt:    BcryptParams{Cost: cost},
	}
}

// withPolicy installs p as the default policy and restores the previous one when
// the test finishes. Tests that call this must NOT use t.Parallel(), since the
// default policy is process-wide global state.
func withPolicy(t *testing.T, p Policy) {
	t.Helper()
	prev := getDefaultPolicy()
	SetDefaultPolicy(p)
	t.Cleanup(func() { SetDefaultPolicy(prev) })
}

// TestHashRoundTripPerAlgorithm covers Hash -> Verify for each shipped
// algorithm: the right password verifies, a wrong one fails, and the stored
// string carries the expected marker. Not parallel: mutates the default policy.
//nolint:paralleltest // mutates the process-wide default policy via withPolicy
func TestHashRoundTripPerAlgorithm(t *testing.T) {
	tests := []struct {
		name      string
		policy    Policy
		wantMatch func(string) bool
	}{
		{
			name:      "argon2id",
			policy:    argon2DefaultPolicy(),
			wantMatch: func(h string) bool { return strings.HasPrefix(h, "$argon2id$") },
		},
		{
			name:      "bcrypt",
			policy:    bcryptPolicy(10),
			wantMatch: isBcryptHash,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)
			withPolicy(t, tc.policy)

			const pw = "correct horse battery staple"
			h, err := Hash(pw)
			r.NoError(err)
			r.NotEmpty(h)
			r.True(tc.wantMatch(h), "unexpected hash marker: %s", h)

			r.True(Verify(pw, h), "correct password should verify")
			r.False(Verify("wrong password", h), "wrong password must not verify")
		})
	}
}

// TestVerifyIsPolicyIndependent proves Verify dispatches on the stored marker,
// not the active policy: an argon2id hash verifies while bcrypt is the policy,
// and vice-versa. This is what keeps Verify safe to read concurrently.
//nolint:paralleltest // mutates the process-wide default policy via withPolicy
func TestVerifyIsPolicyIndependent(t *testing.T) {
	r := require.New(t)

	const pw = "policy-independent-secret"

	// Mint one hash of each algorithm.
	var argonHash, bcryptHash string
	func() {
		withPolicy(t, argon2DefaultPolicy())
		var err error
		argonHash, err = Hash(pw)
		r.NoError(err)
	}()
	func() {
		withPolicy(t, bcryptPolicy(10))
		var err error
		bcryptHash, err = Hash(pw)
		r.NoError(err)
	}()

	// While bcrypt is the active policy, the argon2id hash still verifies.
	withPolicy(t, bcryptPolicy(10))
	r.True(Verify(pw, argonHash), "argon2id hash must verify under bcrypt policy")
	r.True(Verify(pw, bcryptHash), "bcrypt hash must verify under bcrypt policy")

	// And vice-versa under argon2id policy.
	withPolicy(t, argon2DefaultPolicy())
	r.True(Verify(pw, bcryptHash), "bcrypt hash must verify under argon2id policy")
	r.True(Verify(pw, argonHash), "argon2id hash must verify under argon2id policy")
}

// TestNeedsRehash covers the rehash trigger across algorithm and cost changes.
// Not parallel: reads/sets the default policy.
//nolint:paralleltest // mutates the process-wide default policy via withPolicy
func TestNeedsRehash(t *testing.T) {
	r := require.New(t)
	const pw = "rehash-me"

	// Build a hash under each named policy.
	hashUnder := func(p Policy) string {
		withPolicy(t, p)
		h, err := Hash(pw)
		r.NoError(err)
		return h
	}

	lighterArgon := argon2DefaultPolicy()
	lighterArgon.Argon2.Memory = 19456
	lighterArgon.Argon2.Time = 2
	lighterArgon.Argon2.Threads = 1

	argonHash := hashUnder(argon2DefaultPolicy())
	lighterArgonHash := hashUnder(lighterArgon)
	bcryptHash := hashUnder(bcryptPolicy(10))

	t.Run("same algo and params -> false", func(t *testing.T) {
		withPolicy(t, argon2DefaultPolicy())
		require.False(t, NeedsRehash(argonHash))
	})

	t.Run("argon2 cost changed -> true", func(t *testing.T) {
		// Active policy = default argon2; stored hash uses lighter params.
		withPolicy(t, argon2DefaultPolicy())
		require.True(t, NeedsRehash(lighterArgonHash))
	})

	t.Run("algorithm changed argon2->bcrypt -> true", func(t *testing.T) {
		withPolicy(t, bcryptPolicy(12))
		require.True(t, NeedsRehash(argonHash))
	})

	t.Run("algorithm changed bcrypt->argon2 -> true", func(t *testing.T) {
		withPolicy(t, argon2DefaultPolicy())
		require.True(t, NeedsRehash(bcryptHash))
	})

	t.Run("bcrypt cost changed -> true", func(t *testing.T) {
		withPolicy(t, bcryptPolicy(12)) // stored hash is cost 10
		require.True(t, NeedsRehash(bcryptHash))
	})

	t.Run("bcrypt same cost -> false", func(t *testing.T) {
		withPolicy(t, bcryptPolicy(10))
		require.False(t, NeedsRehash(bcryptHash))
	})

	t.Run("unparseable hash -> true", func(t *testing.T) {
		withPolicy(t, argon2DefaultPolicy())
		require.True(t, NeedsRehash("not-a-hash"))
		require.True(t, NeedsRehash(""))
	})
}

// TestBcryptLongAndNulPassword proves the sha256+base64 pre-hash lets bcrypt
// handle passwords longer than 72 bytes and ones containing a NUL byte (both of
// which raw x/crypto bcrypt rejects/truncates). Not parallel: sets the policy.
//nolint:paralleltest // mutates the process-wide default policy via withPolicy
func TestBcryptLongAndNulPassword(t *testing.T) {
	withPolicy(t, bcryptPolicy(10))

	tests := []struct {
		name string
		pw   string
	}{
		{"over 72 bytes", strings.Repeat("a", 100)},
		{"embedded nul", "abc\x00def-tail-that-differs"},
		{"long with nul", strings.Repeat("x", 80) + "\x00" + strings.Repeat("y", 40)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)
			h, err := Hash(tc.pw)
			r.NoError(err)
			r.True(isBcryptHash(h))
			r.True(Verify(tc.pw, h), "long/nul password should verify")
			r.False(Verify(tc.pw+"different", h), "different password must not verify")
		})
	}
}

// TestPolicyFromConfig is covered in the config package for the mapping; here we
// only assert the defensive unknown-algorithm backstop. Not parallel-relevant
// but kept parallel-safe (no global mutation).
func TestPolicyFromConfigUnknownAlgorithm(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	unknownCfg := authConfigWithAlgorithm("scrypt")
	_, err := PolicyFromConfig(&unknownCfg)
	r.ErrorIs(err, ErrUnknownAlgorithm)

	argonCfg := authConfigWithAlgorithm(argon2idID)
	policy, err := PolicyFromConfig(&argonCfg)
	r.NoError(err)
	r.Equal(argon2idID, policy.Algorithm)
}

// TestPolicyFromConfigZeroValueFallsBackToDefaults pins the behavior the
// integration harness relies on: a zero-value AuthConfig (no password block,
// e.g. config.Config built directly in a test) resolves to the legacy argon2id
// default profile rather than erroring on an empty algorithm.
func TestPolicyFromConfigZeroValueFallsBackToDefaults(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var zero config.AuthConfig // Password block is entirely zero-value.
	policy, err := PolicyFromConfig(&zero)
	r.NoError(err)
	r.Equal(argon2idID, policy.Algorithm)
	r.Equal(defaultArgon2Params, policy.Argon2)
	r.Equal(defaultBcryptCost, policy.Bcrypt.Cost)
}
