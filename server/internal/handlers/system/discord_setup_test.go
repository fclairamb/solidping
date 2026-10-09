package system_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/system"
)

func TestDiscordSetupInfo(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	info := system.DiscordSetupInfo("https://status.acme.com")
	r.Equal("https://status.acme.com/api/v1/auth/discord/callback", info.LoginRedirectURI)
	r.Equal("https://status.acme.com/api/v1/integrations/discord/oauth", info.InstallRedirectURI)
	r.Equal("https://status.acme.com/api/v1/integrations/discord/interactions", info.InteractionsURL)
	r.False(info.BaseURLIsDefault)

	r.True(system.DiscordSetupInfo(config.DefaultBaseURL).BaseURLIsDefault)
}
