package config

// DiscordOAuthConfig contains Discord OAuth configuration.
type DiscordOAuthConfig struct {
	ClientID     string `koanf:"client_id"`
	ClientSecret string `koanf:"client_secret"`
	BotToken     string `koanf:"bot_token"`
	RedirectURL  string `koanf:"redirect_url"`
}
