// Package oauth implements the minimal embedded OAuth 2.1 authorization
// server that fronts the MCP resource. SolidPing is both the resource server
// (the MCP endpoint) and the authorization server for that resource: it serves
// the RFC 9728 / RFC 8414 discovery documents, drives the authorization-code +
// PKCE flow against the existing user session, mints audience-bound JWT access
// tokens via the existing auth-service signing key, and supports RFC 7591
// dynamic client registration for native clients (Claude Desktop / mcp-remote).
//
// Scope of v1: a single issuer (cfg.Server.BaseURL); the org is taken from the
// logged-in user's session at /authorize. Tokens reuse the existing HS256 JWT
// machinery — only the discovery, consent, code/token, and DCR surfaces are new.
package oauth

import "strings"

// MCP scope values understood by the authorization server. These mirror the
// scopes the MCP handler consumes (internal/mcp/scope.go) — "mcp" grants full
// tool access, "mcp:read" restricts to non-mutating tools.
const (
	ScopeMCP     = "mcp"
	ScopeMCPRead = "mcp:read"
)

// Discovery document paths (all public, served at the site root rather than
// under /api/v1 so standard MCP clients find them where the specs require).
const (
	// PathProtectedResourceMetadata is the RFC 9728 protected-resource metadata.
	PathProtectedResourceMetadata = "/.well-known/oauth-protected-resource"
	// PathAuthorizationServerMetadata is the RFC 8414 AS metadata.
	PathAuthorizationServerMetadata = "/.well-known/oauth-authorization-server"
	// PathOpenIDConfiguration is the OIDC discovery alias many clients probe.
	PathOpenIDConfiguration = "/.well-known/openid-configuration"
	// PathJWKS publishes the token verification key(s).
	PathJWKS = "/.well-known/jwks.json"

	// PathAuthorize is the authorization endpoint (session-gated).
	PathAuthorize = "/api/v1/oauth/authorize"
	// PathToken is the token endpoint.
	PathToken = "/api/v1/oauth/token"
	// PathRegister is the RFC 7591 dynamic client registration endpoint.
	PathRegister = "/api/v1/oauth/register"
	// PathRevoke is the RFC 7009 token revocation endpoint.
	PathRevoke = "/api/v1/oauth/revoke"

	// PathMCP is the protected MCP resource.
	PathMCP = "/api/v1/mcp"
)

// Metadata centralizes the issuer-derived URLs every handler needs. It is built
// once per request from cfg.Server.BaseURL so a runtime base-URL override (via
// the system-parameters overlay) is always reflected.
type Metadata struct {
	// Issuer is the OAuth issuer identifier — SolidPing's own base URL.
	Issuer string
}

// NewMetadata builds a Metadata from the resolved public base URL. A trailing
// slash on baseURL is trimmed so concatenated paths never double up.
func NewMetadata(baseURL string) Metadata {
	return Metadata{Issuer: strings.TrimRight(baseURL, "/")}
}

// ResourceURL returns the canonical MCP resource identifier used as the token
// audience and advertised in the protected-resource metadata.
func (m Metadata) ResourceURL() string {
	return m.Issuer + PathMCP
}

// AuthorizationEndpoint returns the absolute /authorize URL.
func (m Metadata) AuthorizationEndpoint() string {
	return m.Issuer + PathAuthorize
}

// TokenEndpoint returns the absolute /token URL.
func (m Metadata) TokenEndpoint() string {
	return m.Issuer + PathToken
}

// RegistrationEndpoint returns the absolute /register URL.
func (m Metadata) RegistrationEndpoint() string {
	return m.Issuer + PathRegister
}

// RevocationEndpoint returns the absolute RFC 7009 /revoke URL.
func (m Metadata) RevocationEndpoint() string {
	return m.Issuer + PathRevoke
}

// JWKSURI returns the absolute jwks_uri.
func (m Metadata) JWKSURI() string {
	return m.Issuer + PathJWKS
}

// ProtectedResourceMetadataURL returns the absolute RFC 9728 metadata URL,
// the value advertised in the WWW-Authenticate resource_metadata pointer.
func (m Metadata) ProtectedResourceMetadataURL() string {
	return m.Issuer + PathProtectedResourceMetadata
}

// ProtectedResourceMetadata is the RFC 9728 document served at
// /.well-known/oauth-protected-resource. It tells a client which authorization
// server(s) issue tokens for this resource and which scopes it understands.
//
//nolint:tagliatelle // RFC 9728 wire format requires snake_case field names.
type ProtectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
	BearerMethods        []string `json:"bearer_methods_supported"`
}

// BuildProtectedResourceMetadata assembles the RFC 9728 document.
func (m Metadata) BuildProtectedResourceMetadata() ProtectedResourceMetadata {
	return ProtectedResourceMetadata{
		Resource:             m.ResourceURL(),
		AuthorizationServers: []string{m.Issuer},
		ScopesSupported:      []string{ScopeMCP, ScopeMCPRead},
		BearerMethods:        []string{"header"},
	}
}

// AuthorizationServerMetadata is the RFC 8414 document served at
// /.well-known/oauth-authorization-server (and aliased at
// /.well-known/openid-configuration).
//
//nolint:tagliatelle // RFC 8414 wire format requires snake_case field names.
type AuthorizationServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	RevocationEndpoint                string   `json:"revocation_endpoint"`
	JWKSURI                           string   `json:"jwks_uri"`
	ScopesSupported                   []string `json:"scopes_supported"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
}

// BuildAuthorizationServerMetadata assembles the RFC 8414 document. PKCE is
// S256-only and the implicit grant is deliberately absent (OAuth 2.1).
func (m Metadata) BuildAuthorizationServerMetadata() AuthorizationServerMetadata {
	return AuthorizationServerMetadata{
		Issuer:                            m.Issuer,
		AuthorizationEndpoint:             m.AuthorizationEndpoint(),
		TokenEndpoint:                     m.TokenEndpoint(),
		RegistrationEndpoint:              m.RegistrationEndpoint(),
		RevocationEndpoint:                m.RevocationEndpoint(),
		JWKSURI:                           m.JWKSURI(),
		ScopesSupported:                   []string{ScopeMCP, ScopeMCPRead},
		ResponseTypesSupported:            []string{ResponseTypeCode},
		GrantTypesSupported:               []string{GrantAuthorizationCode, GrantRefreshToken},
		CodeChallengeMethodsSupported:     []string{CodeChallengeMethodS256},
		TokenEndpointAuthMethodsSupported: []string{AuthMethodNone, AuthMethodSecretPost},
	}
}

// Grant types, response types, PKCE method, and token-endpoint auth-method
// constants shared across the package. These are OAuth wire values.
const (
	GrantAuthorizationCode  = "authorization_code"
	GrantRefreshToken       = "refresh_token"
	CodeChallengeMethodS256 = "S256"

	// ResponseTypeCode is the only supported response_type (OAuth 2.1).
	ResponseTypeCode = "code"
	// AuthMethodNone marks a public client (no client secret).
	AuthMethodNone = "none"
	// AuthMethodSecretPost marks a confidential client authenticating with a
	// secret in the token-request body.
	AuthMethodSecretPost = "client_secret_post"
)
