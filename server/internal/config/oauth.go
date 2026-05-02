package config

// GoogleOAuthConfig contains Google OAuth configuration.
type GoogleOAuthConfig struct {
	Enabled      bool   `koanf:"enabled"`
	ClientID     string `koanf:"client_id"`
	ClientSecret string `koanf:"client_secret"`
}
