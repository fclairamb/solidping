package config

import "strings"

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

	// GroupsClaim names the ID token (or userinfo) claim that lists the
	// user's IdP groups (spec 2026-11-21-02). Blank means "groups". The claim
	// may be an array of strings, a single string, or a space-separated string.
	GroupsClaim string `koanf:"groups_claim"`

	// SuperAdminGroups is a comma-separated list of IdP groups whose members
	// are super admins of the instance, synced on every OIDC login. Empty
	// turns the mapping off.
	SuperAdminGroups string `koanf:"super_admin_groups"`

	// OrgAdminGroups is a comma-separated list of IdP groups whose members get
	// the admin role in the organization they log into, joining it if needed.
	// Empty turns the mapping off.
	OrgAdminGroups string `koanf:"org_admin_groups"`
}

// DefaultOIDCGroupsClaim is the claim read when GroupsClaim is blank.
const DefaultOIDCGroupsClaim = "groups"

// Configured reports whether the generic OIDC provider can be used to sign
// in: enabled, with an issuer and a client id/secret pair.
func (c *OIDCOAuthConfig) Configured() bool {
	return c.Enabled && c.IssuerURL != "" && c.ClientID != "" && c.ClientSecret != ""
}

// SuperAdminGroupList returns SuperAdminGroups split on commas, trimmed, with
// blanks dropped.
func (c *OIDCOAuthConfig) SuperAdminGroupList() []string {
	return splitGroupList(c.SuperAdminGroups)
}

// OrgAdminGroupList returns OrgAdminGroups split on commas, trimmed, with
// blanks dropped.
func (c *OIDCOAuthConfig) OrgAdminGroupList() []string {
	return splitGroupList(c.OrgAdminGroups)
}

func splitGroupList(raw string) []string {
	var out []string

	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}

	return out
}
