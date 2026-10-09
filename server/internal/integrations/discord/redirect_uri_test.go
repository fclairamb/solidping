package discord

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/system"
)

// The install URI the dashboard shows must be the one the install flow sends.
func TestInstallRedirectURIMatchesDisplayedURI(t *testing.T) {
	t.Parallel()

	for _, base := range []string{"https://status.acme.com", config.DefaultBaseURL} {
		cfg := &config.Config{}
		cfg.Server.BaseURL = base
		svc := NewService(nil, cfg, nil, nil)

		require.Equal(t, system.DiscordSetupInfo(base).InstallRedirectURI, svc.installRedirectURI())
	}
}
