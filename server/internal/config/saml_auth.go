package config

// SAMLConfig contains SAML 2.0 Service Provider configuration.
//
// Unlike the OAuth2/OIDC-shaped providers, SAML has no client secret: trust
// is established via certificates (the IdP signs assertions with a key the
// SP has been configured to trust, and — optionally — the SP can sign its
// own requests / decrypt encrypted assertions with its own keypair). None of
// the fields below are therefore marked secret in systemconfig.go.
//
// First pass (spec 2026-07-08-08, part 2): global-only configuration (system
// parameters, matching the existing provider pattern), exactly one instance,
// SP-initiated login only (no IdP-initiated, no SLO). See the spec's "Open
// questions" for future per-org / multi-instance extensions.
type SAMLConfig struct {
	Enabled bool `koanf:"enabled"`

	// DisplayName is shown on the login button ("Continue with <DisplayName>")
	// and as the card title in the super-admin config UI. Falls back to "SSO"
	// when blank.
	DisplayName string `koanf:"display_name"`

	// IDPMetadataURL is fetched (and cached in-process) to obtain the IdP's
	// signing certificate, entity ID, and SSO endpoint. At least one of
	// IDPMetadataURL / IDPMetadataXML must be set for login to work; the SP
	// metadata endpoint itself doesn't require either (an admin needs our SP
	// metadata *first*, to configure the IdP side).
	IDPMetadataURL string `koanf:"idp_metadata_url"`

	// IDPMetadataXML is pasted IdP metadata XML, used in place of a fetch
	// when set (no outbound network call needed). Takes precedence over
	// IDPMetadataURL when both are set.
	IDPMetadataXML string `koanf:"idp_metadata_xml"`

	// SPEntityID overrides this SP's entity ID as advertised in its own
	// metadata and used as the SAML Audience value IdPs must restrict
	// assertions to. Defaults to "{BaseURL}/api/v1/auth/saml/metadata" when
	// blank.
	SPEntityID string `koanf:"sp_entity_id"`

	// Attribute mappings: which assertion attributes populate the local
	// user's email/name. Blank falls back to a list of common attribute
	// names/URIs (e.g. "email", "mail", the urn:oid mail attribute) and,
	// failing that, the NameID value when it looks like an email address.
	EmailAttribute string `koanf:"email_attribute"`
	NameAttribute  string `koanf:"name_attribute"`
}
