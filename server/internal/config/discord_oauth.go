package config

import "strings"

// DiscordOAuthConfig contains Discord OAuth configuration.
type DiscordOAuthConfig struct {
	Enabled      bool   `koanf:"enabled"`
	ClientID     string `koanf:"client_id"`
	ClientSecret string `koanf:"client_secret"`
	BotToken     string `koanf:"bot_token"`
	RedirectURL  string `koanf:"redirect_url"`
	// PublicKey is the application's Ed25519 public key (hex), used to verify
	// every inbound interactions request. Discord DEACTIVATES an interactions
	// endpoint whose signature checks fail its probes, so this is mandatory
	// for the bot's buttons and slash commands — not an optional hardening.
	PublicKey string `koanf:"public_key"`
	// GatewayEnabled toggles the Discord Gateway (outgoing WebSocket)
	// supervisor. It is what makes inbound thread replies and mention commands
	// possible at all: Discord has no HTTP event subscription for plain
	// messages. Default false — it needs the privileged MESSAGE_CONTENT
	// intent, which Discord gates behind review once an app reaches 100 guilds.
	GatewayEnabled bool `koanf:"gateway_enabled"`
}

// Environment-variable names for the Discord settings, used to name the
// missing ones in the boot warning. Operators read env vars (or the matching
// system parameters), never the koanf keys.
const (
	EnvDiscordClientID     = "SP_DISCORD_CLIENT_ID"
	EnvDiscordClientSecret = "SP_DISCORD_CLIENT_SECRET"
	EnvDiscordBotToken     = "SP_DISCORD_BOT_TOKEN"
	EnvDiscordPublicKey    = "SP_DISCORD_PUBLIC_KEY"
)

// LoginConfigured reports whether "Sign in with Discord" can complete.
//
// This is the OAuth *login* provider only: a client id and secret are all an
// authorization-code exchange needs. It is deliberately a weaker rule than
// BotConfigured — production ran for months with exactly these three values,
// and sign-in worked.
func (c *DiscordOAuthConfig) LoginConfigured() bool {
	return c.Enabled &&
		strings.TrimSpace(c.ClientID) != "" &&
		strings.TrimSpace(c.ClientSecret) != ""
}

// BotConfigured reports whether the Discord *bot* can complete an install and
// serve interactions.
//
// It needs everything login needs plus the two values that only the bot half
// uses: the bot token (every REST call the bot makes) and the application's
// Ed25519 public key (every inbound interaction is signature-verified, and
// Discord deactivates an endpoint whose signature checks fail its probes).
//
// This is the single definition. Nothing that starts an install round trip —
// a mounted route, a minted URL, a rendered button — may gate on anything
// else: a user must never be sent to Discord for an install this deployment
// cannot finish.
func (c *DiscordOAuthConfig) BotConfigured() bool {
	return c.LoginConfigured() &&
		strings.TrimSpace(c.BotToken) != "" &&
		strings.TrimSpace(c.PublicKey) != ""
}

// MissingBotConfigKeys lists the settings the bot needs and does not have, by
// env-var name, in the order an operator would provision them. Empty when
// BotConfigured() is true; empty too when Discord is disabled outright, since
// nothing is missing from a feature nobody asked for.
func (c *DiscordOAuthConfig) MissingBotConfigKeys() []string {
	if !c.Enabled || c.BotConfigured() {
		return nil
	}

	var missing []string

	for _, candidate := range []struct {
		env   string
		value string
	}{
		{EnvDiscordClientID, c.ClientID},
		{EnvDiscordClientSecret, c.ClientSecret},
		{EnvDiscordBotToken, c.BotToken},
		{EnvDiscordPublicKey, c.PublicKey},
	} {
		if strings.TrimSpace(candidate.value) == "" {
			missing = append(missing, candidate.env)
		}
	}

	return missing
}
