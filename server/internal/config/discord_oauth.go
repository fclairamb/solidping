package config

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
