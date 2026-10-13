package app

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/memlimit"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
)

// smallHostAnswer resolves the small-host flag that passwordPolicyOptions hands
// to PolicyFromConfig, through a config with an implicit argon2 cost.
func smallHostAnswer(t *testing.T, role string) bool {
	t.Helper()

	cfg := &config.Config{}
	cfg.Node.Role = role
	cfg.Auth.Password = config.PasswordConfig{
		Algorithm: config.PasswordAlgorithmArgon2id,
		Argon2:    config.Argon2Params{Memory: 65536, Time: 3, Threads: 4, AutoProfile: true},
	}

	pol, err := passwords.PolicyFromConfig(&cfg.Auth, passwordPolicyOptions(cfg)...)
	require.NoError(t, err)

	return pol.SmallHostProfile
}

// TestPasswordPolicyOptionsRole: only a single-process (role "all") deployment
// may get the small-host profile; split roles keep 64 MiB on any host.
func TestPasswordPolicyOptionsRole(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal(memlimit.SmallHost(), smallHostAnswer(t, config.NodeRoleAll),
		"role all follows the host size")
	r.False(smallHostAnswer(t, config.NodeRoleAPI), "role api never gets the small-host profile")
	r.False(smallHostAnswer(t, "api,jobs"), "split roles never get the small-host profile")
}

// TestLogSmallHostPasswordProfile: one INFO line when the profile is active,
// nothing on a normal host.
func TestLogSmallHostPasswordProfile(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	logSmallHostPasswordProfile(context.Background(), logger, passwords.Policy{
		Algorithm: config.PasswordAlgorithmArgon2id,
		Argon2:    passwords.Argon2Params{Memory: 65536, Time: 3, Threads: 4},
	})
	r.Empty(buf.String(), "no log on a normal host")

	logSmallHostPasswordProfile(context.Background(), logger, passwords.Policy{
		Algorithm:        config.PasswordAlgorithmArgon2id,
		Argon2:           passwords.SmallHostArgon2Params,
		SmallHostProfile: true,
	})
	out := buf.String()
	r.Contains(out, "level=INFO")
	r.Contains(out, "password hashing: argon2id small-host profile m=19456 t=2 p=1")
	r.Contains(out, "<= 2048 MiB; set SP_AUTH_PASSWORD_ARGON2_* to override")
}
