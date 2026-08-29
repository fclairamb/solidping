package slack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSolidpingUsageHintSubcommandsHaveHandlers is the regression test this
// spec exists to add: both manifests advertised a `/solidping` usage hint
// (`[setup|create|list|config|incidents|ack|help]`) while DispatchCommand had
// no `/solidping` case at all, so every one of those subcommands answered
// "Unknown command". Reading the manifest hint and checking each named
// subcommand against solidpingKnownSubcommands — the same map
// handleSolidpingCommand gates dispatch on (solidping_command.go) — makes
// that drift a test failure instead of a silent, user-visible bug: a
// subcommand can be dropped from solidpingKnownSubcommands without anyone
// noticing only if it is also dropped from both manifests in the same change.
func TestSolidpingUsageHintSubcommandsHaveHandlers(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"manifest-dev.json", "manifest-prod.json"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// The package sits four levels below the repository root.
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "wiki", "slack", name))
			require.NoError(t, err)

			var manifest struct {
				Features struct {
					SlashCommands []struct {
						Command   string `json:"command"`
						UsageHint string `json:"usage_hint"`
					} `json:"slash_commands"`
				} `json:"features"`
			}
			require.NoError(t, json.Unmarshal(raw, &manifest))

			var (
				hint  string
				found bool
			)

			for _, sc := range manifest.Features.SlashCommands {
				if sc.Command == "/solidping" {
					hint = sc.UsageHint
					found = true
				}
			}

			require.True(t, found, "%s must register /solidping", name)

			subcommands := strings.Split(strings.Trim(hint, "[]"), "|")
			require.NotEmpty(t, subcommands, "%s: /solidping usage_hint must not be empty", name)

			for _, sub := range subcommands {
				sub = strings.TrimSpace(sub)
				require.NotEmpty(t, sub, "%s: /solidping usage_hint has an empty subcommand entry", name)
				require.True(t, solidpingKnownSubcommands[sub],
					"%s advertises /solidping subcommand %q with no dispatch case in handleSolidpingCommand "+
						"(solidpingKnownSubcommands) — either add the handler or drop it from the hint", name, sub)
			}

			// The manifest must not still register the retired standalone
			// commands: a fresh install should never see them (installs that
			// predate this spec keep them until they re-authorize — see
			// DispatchCommand's "/check"/"/comment" arms).
			for _, sc := range manifest.Features.SlashCommands {
				require.NotContains(t, []string{"/check", "/comment"}, sc.Command,
					"%s must not register the retired standalone %s command", name, sc.Command)
			}
		})
	}
}
