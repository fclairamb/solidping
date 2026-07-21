# MCP & OAuth 2.1

## MCP (Model Context Protocol)

### POST /api/v1/mcp
MCP endpoint for AI tool integrations. Auth: required — either a `mcp`/`mcp:read`-scoped
PAT pasted as a bearer token (back-compat) or an OAuth-issued access token (see OAuth below).
Org is derived from the token. On missing/invalid auth this endpoint returns **401** with a
`WWW-Authenticate: Bearer resource_metadata="<issuer>/.well-known/oauth-protected-resource"`
header so standard MCP clients can discover the authorization server and start the OAuth flow.
OAuth-issued tokens are audience-bound (RFC 8707) to the MCP resource and rejected here if their
`aud` does not include `<issuer>/api/v1/mcp`.

### GET /api/v1/mcp
Probe route. Public — a GET on the MCP endpoint redirects or answers `405` rather than
looking like a missing route, so clients that probe with GET before POSTing get a
useful signal.

## OAuth 2.1 (MCP authorization server)

SolidPing is an embedded OAuth 2.1 authorization server for the MCP resource (spec
2026-06-20-03). Standard MCP clients (Claude Desktop, claude.ai remote connector, `mcp-remote`)
self-onboard via discovery → register → authorize+consent → token, with no hand-pasted token.
Issuer = the SolidPing base URL; the resource is `<issuer>/api/v1/mcp`. Access tokens reuse the
existing HS256 JWT format; refresh tokens rotate. PKCE (`S256`) is mandatory.

### GET /.well-known/oauth-protected-resource
RFC 9728 protected-resource metadata. Public, no auth. Advertises `resource` (the MCP URL),
`authorization_servers` (the issuer), `scopes_supported` (`mcp`, `mcp:read`), and
`bearer_methods_supported`.

### GET /.well-known/oauth-authorization-server
RFC 8414 authorization-server metadata. Public, no auth. Advertises `authorization_endpoint`,
`token_endpoint`, `registration_endpoint`, `jwks_uri`,
`code_challenge_methods_supported=["S256"]`, `grant_types_supported=["authorization_code",
"refresh_token"]`, `response_types_supported=["code"]`, and `scopes_supported=["mcp","mcp:read"]`.

### GET /.well-known/openid-configuration
Alias of the authorization-server metadata (many clients probe this path). Public, no auth.

### GET /.well-known/jwks.json
JWKS endpoint (`jwks_uri`). Public, no auth. The v1 signing key is symmetric (HS256), so the
secret is never published — this serves a well-formed but empty key set. The MCP resource server
validates tokens itself; clients do not verify locally. Asymmetric keys are a documented follow-on.

### POST /api/v1/oauth/register
RFC 7591 dynamic client registration. Public. Accepts `redirect_uris` (required), `client_name`,
`grant_types`, `response_types`, `scope`, `token_endpoint_auth_method`. Native clients are public
(PKCE + loopback redirects `http://127.0.0.1:*` / `http://localhost:*` / `http://[::1]:*`) and get
no secret; a client requesting a secret-based auth method is confidential and gets a `client_secret`
returned once. Redirect URIs must be https or http-loopback. Returns `client_id` (+ `client_secret`
for confidential clients).

### GET /api/v1/oauth/authorize
Authorization endpoint. Requires a logged-in dashboard session (the `access_token` cookie); if
absent, redirects to `/dash0/login?returnTo=…` and back. Validates `client_id`, `redirect_uri`
(exact match against the registered set, loopback ignores the port), `response_type=code`, PKCE
`code_challenge` + `code_challenge_method=S256` (both required; `plain` and missing are rejected),
`scope ⊆ {mcp, mcp:read}`, and `resource` (must equal the MCP resource). On success redirects to the
dashboard consent screen (`/dash0/orgs/:org/oauth/consent`).

### POST /api/v1/oauth/authorize
Consent decision. Re-validates the request, requires a session, and reads `decision` (`approve` /
`deny`). On approve, mints a single-use, short-TTL authorization code bound to
client→redirect→PKCE-challenge→resource→scope→user/org and redirects to the client's `redirect_uri`
with `code` + `state`. On deny, redirects with `error=access_denied`.

### POST /api/v1/oauth/token
Token endpoint (form-encoded). `grant_type=authorization_code` exchanges `code` + `code_verifier`
(+ matching `client_id`, `redirect_uri`) for an access token and a refresh token; the PKCE verifier
is checked against the stored S256 challenge and the code is consumed single-use.
`grant_type=refresh_token` rotates the refresh token (the presented one is revoked atomically and a
new pair issued). Access tokens are JWTs with `aud` = the MCP resource and the consented scopes,
short-lived; refresh tokens are revoked on logout and PAT revoke. Errors use the RFC 6749 §5.2 JSON
shape (`{ "error": "...", "error_description": "..." }`).

### POST /api/v1/oauth/revoke
RFC 7009 token revocation. Public (the token itself is the credential). Revokes the presented
access or refresh token.
