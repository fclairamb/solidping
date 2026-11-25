package integrations

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestCanonicalizeNtfyToken(t *testing.T) {
	t.Parallel()

	t.Run("renames the legacy key without mutating the input", func(t *testing.T) {
		t.Parallel()

		in := map[string]any{"topic": "alerts", "accessToken": "tk_legacy"}

		out := canonicalizeNtfyToken(models.ConnectionTypeNtfy, in)
		require.Equal(t, map[string]any{"topic": "alerts", "auth_token": "tk_legacy"}, out)
		require.Contains(t, in, "accessToken")
	})

	t.Run("keeps an existing auth_token", func(t *testing.T) {
		t.Parallel()

		in := map[string]any{"auth_token": "tk_canonical", "accessToken": "tk_legacy"}

		require.Equal(t, in, canonicalizeNtfyToken(models.ConnectionTypeNtfy, in))
	})

	t.Run("replaces an empty auth_token", func(t *testing.T) {
		t.Parallel()

		out := canonicalizeNtfyToken(models.ConnectionTypeNtfy,
			map[string]any{"auth_token": "", "accessToken": "tk_legacy"})
		require.Equal(t, map[string]any{"auth_token": "tk_legacy"}, out)
	})

	t.Run("ignores settings without the legacy key", func(t *testing.T) {
		t.Parallel()

		in := map[string]any{"topic": "alerts"}

		require.Equal(t, in, canonicalizeNtfyToken(models.ConnectionTypeNtfy, in))
	})

	t.Run("ignores other connection types", func(t *testing.T) {
		t.Parallel()

		in := map[string]any{"accessToken": "tk_legacy"}

		require.Equal(t, in, canonicalizeNtfyToken(models.ConnectionTypeWebhook, in))
	})
}
