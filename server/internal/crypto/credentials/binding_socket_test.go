package credentials_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

// socketBindingCase is a check type whose Unix socket config must bind its
// sealed secrets (spec 2026-11-24-10).
type socketBindingCase struct {
	checkType string
	socketKey string
	// base is the rest of the public config, identical on both sockets.
	base map[string]any
}

func socketBindingCases() []socketBindingCase {
	return []socketBindingCase{
		{checkType: "postgresql", socketKey: "socket", base: map[string]any{"database": "app"}},
		{checkType: "mysql", socketKey: "socket", base: map[string]any{"database": "app"}},
		{checkType: "mongodb", socketKey: "socket", base: map[string]any{"database": "app"}},
		{checkType: "redis", socketKey: "socket", base: map[string]any{}},
		{checkType: "smtp", socketKey: "socket", base: map[string]any{}},
		{checkType: "http", socketKey: "unix_socket", base: map[string]any{"url": "http://localhost/health"}},
	}
}

func withSocket(base map[string]any, key, socket string) map[string]any {
	out := make(map[string]any, len(base)+1)
	for k, v := range base {
		out[k] = v
	}

	out[key] = socket

	return out
}

// TestSealedBindingCoversSocket: a blob sealed for one socket is refused for
// another socket of the same check, and still verifies for its own.
func TestSealedBindingCoversSocket(t *testing.T) {
	t.Parallel()

	for _, tc := range socketBindingCases() {
		t.Run(tc.checkType, func(t *testing.T) {
			t.Parallel()

			sealedFor := withSocket(tc.base, tc.socketKey, "/run/app/a.sock")
			check := configregistry.BindingCheckFor(tc.checkType, sealedFor)
			require.Contains(t, check.TargetFields, tc.socketKey)

			secret := check.SecretFields[0]
			plain := credentials.WithBinding(
				map[string]any{secret: "s3cret"}, tc.checkType, check.TargetFields, sealedFor)

			// Positive control: the unchanged config verifies.
			secrets, bound, err := credentials.VerifyBinding(plain, check)
			require.NoError(t, err)
			require.True(t, bound)
			require.Equal(t, map[string]any{secret: "s3cret"}, secrets)

			// Same blob, another socket: refused.
			moved := withSocket(tc.base, tc.socketKey, "/run/app/b.sock")
			_, _, err = credentials.VerifyBinding(plain, configregistry.BindingCheckFor(tc.checkType, moved))
			require.ErrorIs(t, err, credentials.ErrSealedForAnotherTarget)
		})
	}
}

// TestSealedBindingLegacySocket: a blob digested over the list a type had
// before the socket bound (LegacyTargetFieldsFor) still verifies on a
// network config, but is refused, and listed as stale, once a socket is set.
func TestSealedBindingLegacySocket(t *testing.T) {
	t.Parallel()

	networkConfigs := map[string]map[string]any{
		"postgresql": {"host": "db.acme.com", "port": 5432, "database": "app"},
		"mysql":      {"host": "db.acme.com", "port": 3306, "database": "app"},
		"mongodb":    {"host": "db.acme.com", "port": 27017, "database": "app"},
		"redis":      {"host": "cache.acme.com", "port": 6379},
		"smtp":       {"host": "mail.acme.com", "port": 587},
		"http":       {"url": "https://acme.com/health"},
	}

	for _, tc := range socketBindingCases() {
		t.Run(tc.checkType, func(t *testing.T) {
			t.Parallel()

			cfg, ok := configregistry.ParseConfig(checkerdef.CheckType(tc.checkType))
			require.True(t, ok)

			legacy := credentials.LegacyTargetFieldsFor(cfg)
			require.NotContains(t, legacy, tc.socketKey)

			network := networkConfigs[tc.checkType]
			check := configregistry.BindingCheckFor(tc.checkType, network)
			secret := check.SecretFields[0]

			plain := credentials.WithBinding(map[string]any{secret: "s3cret"}, tc.checkType, legacy, network)
			_, bound, err := credentials.VerifyBinding(plain, check)
			require.NoError(t, err, "a legacy blob of a network config still verifies")
			require.True(t, bound)
			require.False(t, credentials.BindingStale(legacy, check.TargetFields, network))

			socketCfg := withSocket(tc.base, tc.socketKey, "/run/app/a.sock")
			legacyPlain := credentials.WithBinding(
				map[string]any{secret: "s3cret"}, tc.checkType, legacy, socketCfg)
			socketCheck := configregistry.BindingCheckFor(tc.checkType, socketCfg)

			_, _, err = credentials.VerifyBinding(legacyPlain, socketCheck)
			require.ErrorIs(t, err, credentials.ErrSealedForAnotherTarget,
				"a blob digested without the socket is refused once a socket is set")
			require.True(t, credentials.BindingStale(legacy, socketCheck.TargetFields, socketCfg))
		})
	}
}

// TestTCPSocketBindsThroughWildcard: the tcp check declares no target fields
// (and no secrets), so the wildcard binds its socket like any other key.
func TestTCPSocketBindsThroughWildcard(t *testing.T) {
	t.Parallel()

	cfg, ok := configregistry.ParseConfig(checkerdef.CheckType("tcp"))
	require.True(t, ok)
	require.Empty(t, credentials.SecretFieldsFor(cfg))

	fields := credentials.TargetFieldsFor(cfg)
	require.Contains(t, fields, credentials.AllPublicKeys)

	a := map[string]any{"socket": "/run/app/a.sock"}
	b := map[string]any{"socket": "/run/app/b.sock"}
	require.True(t, credentials.TargetChanged(fields, a, b))
}
