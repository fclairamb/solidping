package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkemail/config"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// TestValidateSpecReplacesMalformedToken: only the minted 48-hex shape is kept
// (spec 2026-11-14-01); anything else is replaced by a fresh token, since the
// inbound pattern could never match it.
func TestValidateSpecReplacesMalformedToken(t *testing.T) {
	t.Parallel()

	const minted = "0123456789abcdef0123456789abcdef0123456789abcdef"

	malformed := []any{nil, "", "not-hex", "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789", minted + "00", 42}
	for _, token := range malformed {
		spec := &checkerdef.CheckSpec{Config: map[string]any{"token": token}}
		require.NoError(t, config.ValidateSpec(spec))

		got, _ := spec.Config["token"].(string)
		require.True(t, config.IsValidToken(got), "token %v must be replaced by a minted one, got %q", token, got)
		require.NotEqual(t, token, got)
	}

	spec := &checkerdef.CheckSpec{Config: map[string]any{"token": minted}}
	require.NoError(t, config.ValidateSpec(spec))
	require.Equal(t, minted, spec.Config["token"], "a stored minted token is kept")
}

func TestEmailConfigDeclaresTokenServerManaged(t *testing.T) {
	t.Parallel()

	var cfg checkerdef.Config = &config.EmailConfig{}

	managed, ok := cfg.(checkerdef.ServerManagedFielder)
	require.True(t, ok)
	require.Equal(t, []string{"token"}, managed.ServerManagedFields())
}
