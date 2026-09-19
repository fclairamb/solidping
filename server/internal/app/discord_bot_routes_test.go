package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
)

// Production held SP_DISCORD_ENABLED, SP_DISCORD_CLIENT_ID and
// SP_DISCORD_CLIENT_SECRET — exactly what an OAuth *login* provider needs —
// and neither SP_DISCORD_BOT_TOKEN nor SP_DISCORD_PUBLIC_KEY. The bot's routes
// were mounted anyway, so the dashboard offered an install, sent the user to
// discord.com, and could not complete the round trip (spec 2026-09-19-01).
//
// Every isolated unit test stayed green through all of that, and so did the
// dash0 e2e, because each one runs against a configuration no deployment has.
// The only thing that would have caught it is an assertion about the REAL
// route table at boot — which is this file: real NewServer, real SetupRoutes,
// over the wire, one server per configuration.
const (
	discordRouteOAuthCallback = "/api/v1/integrations/discord/oauth"
	discordRouteInteractions  = "/api/v1/integrations/discord/interactions"
	discordRouteInstallURL    = "/api/v1/orgs/acme/integrations/discord/install-url"
	// Discord login is the control that must NOT move: it is gated on the
	// client id/secret pair alone and production's three values are enough.
	discordRouteLogin = "/api/v1/auth/discord/login"
)

// bootWithDiscord boots a fully wired server over in-memory SQLite with the
// given Discord settings and returns a test server for it.
func bootWithDiscord(t *testing.T, discord config.DiscordOAuthConfig) *httptest.Server {
	t.Helper()
	r := require.New(t)
	ctx := context.Background()

	cfg := &config.Config{}
	cfg.Database.Type = dbTypeSQLiteMemory
	cfg.Auth.JWTSecret = "discord-routes-secret"
	cfg.Server.BaseURL = "https://solidping.example"
	cfg.Discord = discord

	server, err := NewServer(ctx, cfg)
	r.NoError(err)
	t.Cleanup(func() { _ = server.dbService.Close() })

	r.NoError(server.Initialize(ctx))
	r.NoError(server.InitializeSystemConfig(ctx, cfg))
	server.SetupRoutes(ctx)

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	return ts
}

// discordRouteStatus drives one request and reports the status. It never follows
// redirects: a mounted OAuth callback answers with one, and following it would
// turn a "registered" answer into whatever the dashboard replies.
func discordRouteStatus(t *testing.T, ts *httptest.Server, method, path string) int {
	t.Helper()
	r := require.New(t)

	req, err := http.NewRequestWithContext(
		context.Background(), method, ts.URL+path, strings.NewReader("{}"))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Do(req)
	r.NoError(err)

	defer func() { _ = resp.Body.Close() }()

	return resp.StatusCode
}

// notRouted reports whether a status means "no handler is registered here".
// chi answers 404 for an unknown path, but 405 when the path is only reachable
// under another method — the SPA catch-all makes a POST to an unmounted API
// path look like that. Both mean the same thing for this test: nothing is
// mounted. A mounted route always answers something else (a redirect, a 400,
// a 401 from the signature or auth middleware).
func notRouted(status int) bool {
	return status == http.StatusNotFound || status == http.StatusMethodNotAllowed
}

// fullyConfiguredDiscord is what a deployment that can actually run the bot
// looks like.
func fullyConfiguredDiscord() config.DiscordOAuthConfig {
	return config.DiscordOAuthConfig{
		Enabled:      true,
		ClientID:     "discord-client-id",
		ClientSecret: "discord-client-secret",
		BotToken:     "discord-bot-token",
		PublicKey:    "00112233445566778899aabbccddeeff",
	}
}

func TestDiscordBotRoutesFailClosed(t *testing.T) {
	t.Parallel()

	productionState := fullyConfiguredDiscord()
	productionState.BotToken = ""
	productionState.PublicKey = ""

	noBotToken := fullyConfiguredDiscord()
	noBotToken.BotToken = ""

	noPublicKey := fullyConfiguredDiscord()
	noPublicKey.PublicKey = ""

	for name, discord := range map[string]config.DiscordOAuthConfig{
		"the exact production state (login values only)": productionState,
		"bot token missing":  noBotToken,
		"public key missing": noPublicKey,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			ts := bootWithDiscord(t, discord)

			r.True(notRouted(discordRouteStatus(t, ts, http.MethodGet, discordRouteOAuthCallback)),
				"the install callback must not be mounted")
			r.True(notRouted(discordRouteStatus(t, ts, http.MethodPost, discordRouteInteractions)),
				"the interactions endpoint must not be mounted without a public key to verify with")
			r.True(notRouted(discordRouteStatus(t, ts, http.MethodPost, discordRouteInstallURL)),
				"the endpoint that mints the install URL must not be mounted")

			// Discord LOGIN is untouched: it needs the client id/secret pair
			// and nothing else, and it genuinely works on production's three
			// values. A fix that quietly took sign-in away would be a worse
			// regression than the bug.
			r.False(notRouted(discordRouteStatus(t, ts, http.MethodGet, discordRouteLogin)),
				"Discord login must keep working")
		})
	}
}

// Positive control. Without it, a test that asserts 404 everywhere would stay
// green if routing broke altogether, or if the predicate were accidentally
// hardcoded to false.
func TestDiscordBotRoutesMountWhenFullyConfigured(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ts := bootWithDiscord(t, fullyConfiguredDiscord())

	r.False(notRouted(discordRouteStatus(t, ts, http.MethodGet, discordRouteOAuthCallback)),
		"the install callback must be mounted when the bot is fully configured")
	r.False(notRouted(discordRouteStatus(t, ts, http.MethodPost, discordRouteInteractions)),
		"the interactions endpoint must be mounted when the bot is fully configured")
	r.False(notRouted(discordRouteStatus(t, ts, http.MethodPost, discordRouteInstallURL)),
		"the install-URL endpoint must be mounted when the bot is fully configured")
}

// Discord off entirely: nothing at all, login included.
func TestDiscordRoutesAbsentWhenDisabled(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ts := bootWithDiscord(t, config.DiscordOAuthConfig{})

	r.True(notRouted(discordRouteStatus(t, ts, http.MethodGet, discordRouteOAuthCallback)))
	r.True(notRouted(discordRouteStatus(t, ts, http.MethodGet, discordRouteLogin)))
}
