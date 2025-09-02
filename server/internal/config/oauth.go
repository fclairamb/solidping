package config

// GoogleOAuthConfig contains Google OAuth configuration.
type GoogleOAuthConfig struct {
	ClientID     string `koanf:"client_id"`
	ClientSecret string `koanf:"client_secret"`
}
