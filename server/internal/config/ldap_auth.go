package config

// LDAPConfig contains LDAP / Active Directory bind-authentication
// configuration.
//
// Unlike the OAuth2/OIDC- and SAML-shaped providers, LDAP is not a
// redirect/callback mechanism: it plugs into the existing password-login
// path (POST /api/v1/auth/login) as an alternate/fallback credential
// verification mechanism — see the fallback ordering documented on
// Service.Login in internal/handlers/auth/service.go.
//
// First pass (spec 2026-07-08-08, part 3): global-only configuration
// (system parameters, matching the existing provider pattern) and exactly
// one connector. Per-org configuration, multiple connectors, and group/role
// mapping are explicit future extensions — see the spec's "Open questions".
type LDAPConfig struct {
	Enabled bool `koanf:"enabled"`

	// ServerURL is the directory address, e.g. "ldap://ldap.example.com:389"
	// or "ldaps://ldap.example.com:636". Use ldaps:// (or StartTLS below)
	// for anything but local testing — credentials otherwise cross the wire
	// in the clear.
	ServerURL string `koanf:"server_url"`

	// StartTLS upgrades a plain "ldap://" connection to TLS before any bind
	// is attempted. Ignored for "ldaps://" URLs, which are already TLS from
	// the first byte.
	StartTLS bool `koanf:"start_tls"`

	// InsecureSkipVerify disables TLS certificate verification for ldaps://
	// / StartTLS connections. Only ever intended for local testing against
	// a self-signed directory certificate.
	InsecureSkipVerify bool `koanf:"insecure_skip_verify"`

	// BindDN / BindPassword are the service account used to search the
	// directory before the actual end-user bind (search-then-bind).
	// BindPassword is a secret system parameter. When BindDN is blank, the
	// search is performed over an unauthenticated connection (some
	// directories explicitly allow anonymous read/search) — this is
	// unrelated to, and must never be confused with, the empty-password
	// guard on the end-user credential bind (see ldap_service.go).
	BindDN       string `koanf:"bind_dn"`
	BindPassword string `koanf:"bind_password"`

	// BaseDN is the search base, e.g. "ou=people,dc=example,dc=com".
	BaseDN string `koanf:"base_dn"`

	// UserFilter is a filter template with exactly one "%s" placeholder for
	// the submitted identifier, e.g. "(mail=%s)" or "(sAMAccountName=%s)".
	// The submitted value is always RFC 4515-escaped before being
	// substituted in — see buildUserFilter in ldap_service.go — so the
	// template itself may safely be an arbitrary admin-authored filter,
	// including a composite "(&(objectClass=...)(mail=%s))" one. Defaults
	// to "(mail=%s)" when blank.
	UserFilter string `koanf:"user_filter"`

	// Attribute mappings: which directory attributes populate the local
	// user's email/name. Blank falls back to "mail"/"cn".
	EmailAttribute string `koanf:"email_attribute"`
	NameAttribute  string `koanf:"name_attribute"`
}
