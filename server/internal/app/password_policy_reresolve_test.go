package app

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// argon2Policy builds an argon2id policy with the given memory cost, leaving the
// rest at the shipped defaults.
func argon2PolicyWithMemory(memory uint32) passwords.Policy {
	return passwords.Policy{
		Algorithm: config.PasswordAlgorithmArgon2id,
		Argon2: passwords.Argon2Params{
			Memory: memory, Time: 3, Threads: 4, KeyLength: 32, SaltLength: 16,
		},
		Bcrypt:        passwords.BcryptParams{Cost: 12},
		RehashOnLogin: true,
	}
}

// activeArgon2Memory mints a hash under the currently-installed default policy
// and extracts the encoded m= cost, i.e. the active argon2 memory parameter.
func activeArgon2Memory(t *testing.T) string {
	t.Helper()
	h, err := passwords.Hash("observe-active-policy")
	require.NoError(t, err)
	// argon2id hashes encode params as: $argon2id$v=19$m=65536,t=3,p=4$...
	for _, seg := range strings.Split(h, "$") {
		if strings.HasPrefix(seg, "m=") {
			mem, _, _ := strings.Cut(seg, ",")
			return mem
		}
	}
	t.Fatalf("no m= segment in hash %q", h)

	return ""
}

// TestReResolvePasswordPolicy covers the startup re-resolve (spec Q2): after the
// system-parameter overlay mutates cfg, the active policy must reflect a valid DB
// value; an INVALID overlaid value must warn and retain the prior policy without
// aborting. Not parallel: mutates the process-wide default policy.
//
//nolint:paralleltest // mutates the process-wide default password policy
func TestReResolvePasswordPolicy(t *testing.T) {
	ctx := context.Background()

	// Restore the shipped default policy when the test finishes.
	prior := argon2PolicyWithMemory(64 * 1024)
	passwords.SetDefaultPolicy(prior)
	t.Cleanup(func() { passwords.SetDefaultPolicy(prior) })

	t.Run("valid overlaid value is installed", func(t *testing.T) {
		r := require.New(t)
		passwords.SetDefaultPolicy(prior)

		cfg := &config.Config{}
		cfg.Auth.Password = config.PasswordConfig{
			Algorithm: config.PasswordAlgorithmArgon2id,
			Argon2: config.Argon2Params{
				Memory: 19456, Time: 2, Threads: 1, KeyLength: 32, SaltLength: 16,
			},
			Bcrypt:        config.BcryptParams{Cost: 12},
			RehashOnLogin: true,
		}

		reResolvePasswordPolicy(ctx, cfg)

		r.Equal("m=19456", activeArgon2Memory(t), "active policy must reflect the overlaid DB value")
	})

	t.Run("invalid overlaid value retains prior policy without aborting", func(t *testing.T) {
		r := require.New(t)
		// Install a known-good prior policy (m=65536).
		passwords.SetDefaultPolicy(prior)

		cfg := &config.Config{}
		cfg.Auth.Password = config.PasswordConfig{
			Algorithm: config.PasswordAlgorithmArgon2id,
			Argon2: config.Argon2Params{
				// Memory below the hard floor (8192 KiB) — must be rejected.
				Memory: 1024, Time: 3, Threads: 4, KeyLength: 32, SaltLength: 16,
			},
			Bcrypt:        config.BcryptParams{Cost: 12},
			RehashOnLogin: true,
		}

		// Must not panic / abort; it simply warns and keeps the prior policy.
		reResolvePasswordPolicy(ctx, cfg)

		r.Equal("m=65536", activeArgon2Memory(t), "invalid value must not replace the prior policy")
	})

	t.Run("unknown algorithm retains prior policy", func(t *testing.T) {
		r := require.New(t)
		passwords.SetDefaultPolicy(prior)

		cfg := &config.Config{}
		cfg.Auth.Password = config.PasswordConfig{Algorithm: "scrypt"}

		reResolvePasswordPolicy(ctx, cfg)

		r.Equal("m=65536", activeArgon2Memory(t), "unknown algorithm must not replace the prior policy")
	})
}
