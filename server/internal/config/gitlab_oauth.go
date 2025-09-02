package config

// GitLabOAuthConfig contains GitLab OAuth configuration.
type GitLabOAuthConfig struct {
	ClientID     string `koanf:"client_id"`
	ClientSecret string `koanf:"client_secret"`
	BaseURL      string `koanf:"base_url"` // Defaults to "https://gitlab.com" for gitlab.com
}
