package config

// OIDCOAuthConfig contains generic OpenID Connect provider configuration.
//
// Unlike the six bespoke social providers (Google, GitHub, GitLab, Microsoft,
// Slack, Discord), this connects to an arbitrary OAuth2/OIDC identity
// provider (Keycloak, Authentik, Okta, Auth0, a corporate Azure AD tenant,
// ...) via standard OIDC discovery (`{IssuerURL}/.well-known/openid-configuration`).
//
// First pass (spec 2026-07-08-08, part 1): global-only configuration (system
// parameters, matching the existing provider pattern) and exactly one
// instance. Per-org configuration and multiple instances are explicit future
// extensions — see the spec's "Open questions".
type OIDCOAuthConfig struct {
	Enabled bool `koanf:"enabled"`

	// DisplayName is shown on the login button ("Continue with <DisplayName>")
	// and as the card title in the super-admin config UI. Falls back to "SSO"
	// when blank.
	DisplayName string `koanf:"display_name"`

	// IssuerURL is the OIDC issuer, e.g. "https://idp.example.com/realms/acme".
	// Discovery is performed against "{IssuerURL}/.well-known/openid-configuration".
	IssuerURL    string `koanf:"issuer_url"`
	ClientID     string `koanf:"client_id"`
	ClientSecret string `koanf:"client_secret"`

	// Scopes is a space-separated OAuth2 scope list, e.g. "openid email profile".
	// "openid" is always implicitly included; defaults to "openid email profile"
	// when blank.
	Scopes string `koanf:"scopes"`

	// Claim mappings: which ID token claims populate the local user record.
	// Blank falls back to the OIDC standard claim name (email / name / picture).
	EmailClaim  string `koanf:"email_claim"`
	NameClaim   string `koanf:"name_claim"`
	AvatarClaim string `koanf:"avatar_claim"`
}
