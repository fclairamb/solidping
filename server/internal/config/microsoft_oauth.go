package config

// MicrosoftOAuthConfig contains Microsoft (Entra ID) OAuth configuration.
type MicrosoftOAuthConfig struct {
	ClientID     string `koanf:"client_id"`
	ClientSecret string `koanf:"client_secret"`
	TenantID     string `koanf:"tenant_id"` // Defaults to "common" for multi-tenant
}
