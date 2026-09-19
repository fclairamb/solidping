package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The two halves of "Discord" need different things, and production proved it
// the hard way: it carried the client id/secret pair (login worked, a user
// signed up through it) and neither the bot token nor the application public
// key, while the bot was advertised anyway (spec 2026-09-19-01).
func TestDiscordConfiguredPredicates(t *testing.T) {
	t.Parallel()

	full := DiscordOAuthConfig{
		Enabled:      true,
		ClientID:     "id",
		ClientSecret: "secret",
		BotToken:     "token",
		PublicKey:    "pubkey",
	}

	production := full
	production.BotToken = ""
	production.PublicKey = ""

	withWhitespace := full
	withWhitespace.BotToken = "   "

	disabled := full
	disabled.Enabled = false

	for name, testCase := range map[string]struct {
		cfg     DiscordOAuthConfig
		login   bool
		bot     bool
		missing []string
	}{
		"fully configured": {cfg: full, login: true, bot: true},
		"the production state: login values only": {
			cfg: production, login: true, bot: false,
			missing: []string{EnvDiscordBotToken, EnvDiscordPublicKey},
		},
		"bot token missing": {
			cfg:   func() DiscordOAuthConfig { c := full; c.BotToken = ""; return c }(),
			login: true, bot: false, missing: []string{EnvDiscordBotToken},
		},
		"public key missing": {
			cfg:   func() DiscordOAuthConfig { c := full; c.PublicKey = ""; return c }(),
			login: true, bot: false, missing: []string{EnvDiscordPublicKey},
		},
		"client secret missing": {
			cfg:   func() DiscordOAuthConfig { c := full; c.ClientSecret = ""; return c }(),
			login: false, bot: false, missing: []string{EnvDiscordClientSecret},
		},
		"whitespace is not a value": {
			cfg: withWhitespace, login: true, bot: false,
			missing: []string{EnvDiscordBotToken},
		},
		// Nothing is "missing" from a feature the operator never asked for:
		// a self-hosted install with Discord off must log no warning at all.
		"disabled entirely": {cfg: disabled, login: false, bot: false},
		"empty":             {cfg: DiscordOAuthConfig{}, login: false, bot: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			cfg := testCase.cfg
			r.Equal(testCase.login, cfg.LoginConfigured(), "LoginConfigured")
			r.Equal(testCase.bot, cfg.BotConfigured(), "BotConfigured")
			r.Equal(testCase.missing, cfg.MissingBotConfigKeys(), "MissingBotConfigKeys")
		})
	}
}

// The bot rule is strictly stronger than the login rule. If that inverts, a
// deployment could mount the bot without being able to sign anybody in.
func TestDiscordBotConfiguredImpliesLoginConfigured(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := DiscordOAuthConfig{
		Enabled:      true,
		ClientID:     "id",
		ClientSecret: "secret",
		BotToken:     "token",
		PublicKey:    "pubkey",
	}

	r.True(cfg.BotConfigured())
	r.True(cfg.LoginConfigured())

	cfg.ClientSecret = ""
	r.False(cfg.BotConfigured(), "the bot must need everything login needs")
}
