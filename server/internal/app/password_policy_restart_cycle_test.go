package app

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// encodedArgon2Params mints a hash under the active default policy and returns
// the "m=..,t=..,p=.." cost segment it encodes, so a test can assert the freshly
// hashed password reflects the persisted parameters.
func encodedArgon2Params(t *testing.T) string {
	t.Helper()
	h, err := passwords.Hash("restart-cycle-observe")
	require.NoError(t, err)
	// $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>
	for _, seg := range strings.Split(h, "$") {
		if strings.HasPrefix(seg, "m=") {
			return seg
		}
	}
	t.Fatalf("no m= segment in hash %q", h)

	return ""
}

// TestPasswordPolicyFullRestartCycle exercises the whole chain the spec
// describes for an operator changing the hashing profile from the Server
// Settings UI: a value persisted to the systemconfig/DB layer ->
// "restart" (re-run the systemconfig overlay onto a fresh cfg, then re-resolve
// the policy) -> a freshly hashed password encodes the NEW parameters. The
// pieces are unit-tested elsewhere; this asserts the end-to-end wiring through
// the real DB and the real overlay, not a hand-built cfg.
//
// It also covers the safety property: an INVALID persisted value must keep the
// prior policy without aborting the "restart".
//
// Not parallel: mutates the process-wide default password policy.
//
//nolint:paralleltest // mutates the process-wide default password policy
func TestPasswordPolicyFullRestartCycle(t *testing.T) {
	ctx := context.Background()

	// Restore the shipped default policy when the test finishes.
	prior := argon2PolicyWithMemory(64 * 1024)
	passwords.SetDefaultPolicy(prior)
	t.Cleanup(func() { passwords.SetDefaultPolicy(prior) })

	// simulateRestart re-resolves the password policy exactly as a real boot
	// does: build a fresh zero-value cfg, run the systemconfig overlay (env > DB
	// > default) against the persisted parameters, then re-install the policy.
	simulateRestart := func(t *testing.T, dbSvc *sqlite.Service) {
		t.Helper()
		cfg := &config.Config{}
		r := require.New(t)
		r.NoError(systemconfig.NewService(dbSvc, cfg).Initialize(ctx))
		reResolvePasswordPolicy(ctx, cfg)
	}

	t.Run("persisted params take effect after restart", func(t *testing.T) {
		r := require.New(t)

		// Start from a known prior profile (m=65536) so we can prove the change.
		passwords.SetDefaultPolicy(prior)

		dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
		r.NoError(err)
		r.NoError(dbSvc.Initialize(ctx))
		t.Cleanup(func() { _ = dbSvc.Close() })

		// Persist the new profile to the DB exactly as the UI's PUT would: the
		// dashboard writes the algorithm plus the full active-algorithm param set
		// in one batch, so the resolved block is complete and valid (the OWASP
		// 19 MiB / t2 / p1 argon2id profile, with the default key/salt lengths).
		r.NoError(dbSvc.SetSystemParameter(ctx, string(systemconfig.KeyPasswordAlgorithm), "argon2id", false))
		r.NoError(dbSvc.SetSystemParameter(ctx, string(systemconfig.KeyPasswordArgon2Memory), float64(19456), false))
		r.NoError(dbSvc.SetSystemParameter(ctx, string(systemconfig.KeyPasswordArgon2Time), float64(2), false))
		r.NoError(dbSvc.SetSystemParameter(ctx, string(systemconfig.KeyPasswordArgon2Threads), float64(1), false))
		r.NoError(dbSvc.SetSystemParameter(ctx, string(systemconfig.KeyPasswordArgon2KeyLen), float64(32), false))
		r.NoError(dbSvc.SetSystemParameter(ctx, string(systemconfig.KeyPasswordArgon2SaltLen), float64(16), false))

		simulateRestart(t, dbSvc)

		// A password hashed after the restart must encode the persisted params.
		r.Equal("m=19456,t=2,p=1", encodedArgon2Params(t),
			"a freshly hashed password must encode the persisted parameters")
	})

	t.Run("invalid persisted value keeps prior policy without aborting", func(t *testing.T) {
		r := require.New(t)

		// Prior policy is m=65536.
		passwords.SetDefaultPolicy(prior)

		dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
		r.NoError(err)
		r.NoError(dbSvc.Initialize(ctx))
		t.Cleanup(func() { _ = dbSvc.Close() })

		// Persist an otherwise-complete argon2 profile whose only fault is a
		// sub-floor memory (1024 KiB < 8192 KiB hard floor) — the kind of value
		// that could only land via the raw API past the write validation. The
		// restart must not abort; it warns and keeps the prior policy.
		r.NoError(dbSvc.SetSystemParameter(ctx, string(systemconfig.KeyPasswordAlgorithm), "argon2id", false))
		r.NoError(dbSvc.SetSystemParameter(ctx, string(systemconfig.KeyPasswordArgon2Memory), float64(1024), false))
		r.NoError(dbSvc.SetSystemParameter(ctx, string(systemconfig.KeyPasswordArgon2Time), float64(2), false))
		r.NoError(dbSvc.SetSystemParameter(ctx, string(systemconfig.KeyPasswordArgon2Threads), float64(1), false))
		r.NoError(dbSvc.SetSystemParameter(ctx, string(systemconfig.KeyPasswordArgon2KeyLen), float64(32), false))
		r.NoError(dbSvc.SetSystemParameter(ctx, string(systemconfig.KeyPasswordArgon2SaltLen), float64(16), false))

		simulateRestart(t, dbSvc)

		r.Equal("m=65536,t=3,p=4", encodedArgon2Params(t),
			"an invalid persisted value must not replace the prior policy")
	})
}
