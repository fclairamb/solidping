package config

// GitHubOAuthConfig contains GitHub OAuth configuration.
type GitHubOAuthConfig struct {
	Enabled      bool   `koanf:"enabled"`
	ClientID     string `koanf:"client_id"`
	ClientSecret string `koanf:"client_secret"`
}
