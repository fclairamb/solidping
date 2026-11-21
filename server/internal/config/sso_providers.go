package config

// SSOFallbackLabel is the display name of a SAML/OIDC provider configured
// without one.
const SSOFallbackLabel = "SSO"

// SSOProvider is a federated sign-in provider the login page offers.
type SSOProvider struct {
	Name string
	Type string
}

// SSOProviders returns the federated sign-in providers that are configured and
// enabled, in the order the login page shows them. It is the single source of
// truth for both GET /auth/providers and the local-login lock-out guard (spec
// 2026-11-21-02): local login may only be switched off while this is non-empty.
//
//nolint:cyclop // Linear sequence of provider checks with the same shape.
func (c *Config) SSOProviders() []SSOProvider {
	providers := make([]SSOProvider, 0)

	if c.Slack.Enabled && c.Slack.ClientID != "" && c.Slack.ClientSecret != "" {
		providers = append(providers, SSOProvider{Name: "Slack", Type: "slack"})
	}

	if c.Google.Enabled && c.Google.ClientID != "" && c.Google.ClientSecret != "" {
		providers = append(providers, SSOProvider{Name: "Google", Type: "google"})
	}

	if c.GitHub.Enabled && c.GitHub.ClientID != "" && c.GitHub.ClientSecret != "" {
		providers = append(providers, SSOProvider{Name: "GitHub", Type: "github"})
	}

	if c.Microsoft.Enabled && c.Microsoft.ClientID != "" && c.Microsoft.ClientSecret != "" {
		providers = append(providers, SSOProvider{Name: "Microsoft", Type: "microsoft"})
	}

	if c.GitLab.Enabled && c.GitLab.ClientID != "" && c.GitLab.ClientSecret != "" {
		providers = append(providers, SSOProvider{Name: "GitLab", Type: "gitlab"})
	}

	// Login needs only the client id/secret pair — deliberately a weaker rule
	// than DiscordOAuthConfig.BotConfigured(), which the bot half gates on.
	if c.Discord.LoginConfigured() {
		providers = append(providers, SSOProvider{Name: "Discord", Type: "discord"})
	}

	// Telegram is not OAuth: it needs the instance bot plus the explicit
	// SP_TELEGRAM_LOGIN_ENABLED opt-in (the bot's domain is set by hand).
	if c.Telegram.LoginActive() {
		providers = append(providers, SSOProvider{Name: "Telegram", Type: "telegram"})
	}

	if c.OIDC.Configured() {
		providers = append(providers, SSOProvider{Name: labelOr(c.OIDC.DisplayName), Type: "oidc"})
	}

	// SAML's wire protocol differs from the OAuth-shaped providers above
	// (POST-back ACS instead of a GET callback, a metadata endpoint instead
	// of a client id/secret pair), but from the login page's point of view
	// it's the same shape: a full-page redirect and back with tokens in the
	// query string.
	if c.SAML.Enabled && (c.SAML.IDPMetadataURL != "" || c.SAML.IDPMetadataXML != "") {
		providers = append(providers, SSOProvider{Name: labelOr(c.SAML.DisplayName), Type: "saml"})
	}

	return providers
}

func labelOr(name string) string {
	if name == "" {
		return SSOFallbackLabel
	}

	return name
}
