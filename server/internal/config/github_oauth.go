package config

// GitHubOAuthConfig contains GitHub OAuth configuration.
type GitHubOAuthConfig struct {
	ClientID     string `koanf:"client_id"`
	ClientSecret string `koanf:"client_secret"`
}
