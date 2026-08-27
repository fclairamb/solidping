package slack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestBotScopesMatchManifests pins the install-time scope lists to the app
// manifests.
//
// The two drifted apart once already, silently and for months: the manifests
// declared channels:history, groups:history, im:write and links:write while
// BuildInstallURL never requested them, so thread-reply ingestion, bot DMs and
// link unfurling were dead on every install that went through our own OAuth
// URL. A scope present in only one of the two places is always a bug — never
// granted if it is manifest-only, rejected at authorize time if it is
// code-only.
func TestBotScopesMatchManifests(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"manifest-dev.json", "manifest-prod.json"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// The package sits four levels below the repository root.
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "wiki", "slack", name))
			require.NoError(t, err)

			var manifest struct {
				OAuthConfig struct {
					Scopes struct {
						Bot  []string `json:"bot"`
						User []string `json:"user"`
					} `json:"scopes"`
				} `json:"oauth_config"` //nolint:tagliatelle // Slack app manifests use snake_case
			}
			require.NoError(t, json.Unmarshal(raw, &manifest))

			require.ElementsMatch(t, manifest.OAuthConfig.Scopes.Bot, slackBotScopes,
				"bot scopes in %s must match slackBotScopes", name)
			require.ElementsMatch(t, manifest.OAuthConfig.Scopes.User, slackUserScopes,
				"user scopes in %s must match slackUserScopes", name)
		})
	}
}
