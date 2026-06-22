# OAuth 2.1 authorization for the MCP endpoint

> **Recommended choices applied 2026-06-20** (embedded AS + DCR in scope — see **Decisions**).
> Four consequential forks had *no* recommendation in the original spec and are deliberately left
> open with a noted leaning — see **Remaining open questions**. This feature still needs the
> flagged security review before build.

## Context

Inspired by the Maintenant competitor analysis ([`wiki/competitors/maintenant.md`](../../docs/competitors/maintenant.md)):
both products ship an MCP server (a rare overlap), but Maintenant lists **OAuth2 on the MCP
endpoint** as an advantage. SolidPing's MCP auth today is bearer-token only:

- MCP is mounted at `POST /api/v1/mcp` under `authMiddleware.RequireAuth`
  ([`app/server.go:486-487`](../../server/internal/app/server.go)).
- The handler reads claims from context and requires the **`mcp`** or **`mcp:read`** scope
  ([`mcp/handler.go:149-158`](../../server/internal/mcp/handler.go),
  [`mcp/scope.go`](../../server/internal/mcp/scope.go)). The token is a normal SolidPing JWT/PAT
  whose scopes live in `Claims.Scopes` ([`handlers/auth/service.go:140-145`](../../server/internal/handlers/auth/service.go)).
- Transport is MCP **Streamable HTTP**, protocol `2025-03-26`, session via `Mcp-Session-Id`
  ([`mcp/handler.go:39-63`, `241`](../../server/internal/mcp/handler.go)).

What this means in practice: a user must **manually mint a PAT** with the `mcp` scope and paste
it into their MCP client as a static bearer token. That works, but **no standard MCP client can
self-onboard** — Claude Desktop / claude.ai remote connectors / `mcp-remote` expect the MCP
**OAuth 2.1 authorization flow** (discovery → authorize → token), not a hand-pasted secret.

**SolidPing is currently an OAuth *client*, not an authorization *server*.** It does social
login *as a client* (Slack/Google/GitHub — [`app/server.go:420-440`](../../server/internal/app/server.go)),
but exposes **no** `/.well-known/oauth-*` endpoints (grep is negative) and issues no
authorization-code grants to third-party apps. So this is net-new authorization-server work.

### What the MCP authorization spec requires

Per the MCP authorization spec (rev `2025-03-26`, hardened in `2025-06-18`), the MCP server is an
**OAuth 2.0 Resource Server** and clients drive an **OAuth 2.1** flow:

1. **Protected Resource Metadata** (RFC 9728) at
   `/.well-known/oauth-protected-resource`, listing the `authorization_servers`.
2. On an unauthenticated request, **`401` with `WWW-Authenticate: Bearer
   resource_metadata="…/.well-known/oauth-protected-resource"`** so the client can discover (2).
3. **Authorization Server Metadata** (RFC 8414) at `/.well-known/oauth-authorization-server`
   (clients also probe `/.well-known/openid-configuration`).
4. **Authorization Code + PKCE** (`S256`) — no implicit grant.
5. **Resource Indicators** (RFC 8707): the `resource` parameter binds the issued token's
   **audience** to this MCP server so a token for one resource can't be replayed at another.
6. **Dynamic Client Registration** (RFC 7591) at a `registration_endpoint` — Claude Desktop and
   `mcp-remote` have no pre-issued `client_id`, so without DCR they cannot connect at all.

## My honest opinion

You asked for "OAuth2 for MCP." Here's the shape I'd commit to.

**1. Make SolidPing a *minimal embedded* OAuth 2.1 AS for the MCP resource — don't delegate.**
SolidPing is self-hosted-first and must work with **no external IdP**. So the authorization
server should be SolidPing itself: reuse the **existing user login/session** for the
`/authorize` step (+ a consent screen), and reuse the **existing JWT + `Claims.Scopes`
machinery** ([`handlers/auth/service.go`](../../server/internal/handlers/auth/service.go)) to mint
access tokens carrying `mcp` / `mcp:read`. We are not building a general-purpose OAuth provider —
just the slice MCP needs. Delegation to an org's external IdP is a *later* option, not v1.

**2. OAuth is additive — keep the PAT-bearer path working.** Existing setups that paste an
`mcp`-scoped PAT must keep working unchanged. The MCP handler already accepts any valid
JWT/PAT with the right scope; OAuth just adds a second, self-service way to *obtain* such a token.

**3. Phase it, because this is genuinely large.** The single most valuable early slice is
**discovery + audience-bound validation** (clients can *see* that OAuth exists and tokens are
correctly scoped to the MCP resource). The authorization endpoints + DCR are the heavier lifts.
A half-built AS is a security liability, so each phase must be complete on its own.

**4. DCR is not optional if the goal is "Claude Desktop just works."** Targeting those clients
*requires* RFC 7591. If we only want first-party / scripted clients, we can skip DCR and use
pre-registered clients — that's the key scoping decision below.

**5. This needs a security review.** We'd be turning SolidPing into a token issuer for external
clients: PKCE enforcement, redirect-URI validation (incl. the loopback exception native clients
need), consent, audience binding, short-lived tokens + refresh rotation. Flagged explicitly.

## Goals

- Standard MCP clients can connect to `/api/v1/mcp` via the OAuth 2.1 flow with **no
  hand-pasted token**.
- The MCP endpoint behaves as a proper OAuth Resource Server: 401 + `WWW-Authenticate`
  discovery pointer, audience-bound token validation.
- Tokens issued through the flow carry `mcp` / `mcp:read` (consented) and are audience-bound to
  the MCP resource.
- Existing PAT-bearer auth keeps working (back-compat).

## Out of scope

- A general-purpose OAuth 2.1 authorization server for arbitrary third-party apps beyond MCP.
- Delegating MCP auth to an org's external IdP (Google/GitHub/OIDC) — a later option.
- Replacing the dashboard's existing JWT/refresh session auth.
- SCIM / enterprise SSO.

## Design (phased)

### Phase 1 — Resource Server discovery + audience validation (no new grants)

- Serve **Protected Resource Metadata** at `/.well-known/oauth-protected-resource` (public, no
  auth) advertising `resource` = the MCP URL and `authorization_servers` = SolidPing's issuer.
- In the MCP auth path, when auth is missing/invalid, return **`401` with `WWW-Authenticate:
  Bearer resource_metadata="…"`** (today it returns a plain 401 JSON at
  [`mcp/handler.go:149-153`](../../server/internal/mcp/handler.go)). This is the discovery hook.
- Extend token validation so MCP access tokens are **audience-checked** against the MCP resource
  (RFC 8707). PATs (which have no `aud`) continue to pass via the existing scope check for
  back-compat.

### Phase 2 — Embedded authorization server

- `/.well-known/oauth-authorization-server` (RFC 8414) advertising `authorization_endpoint`,
  `token_endpoint`, `jwks_uri`, supported PKCE methods (`S256`), grant types (`authorization_code`,
  `refresh_token`), scopes (`mcp`, `mcp:read`).
- **`/authorize`**: requires a logged-in SolidPing user (reuse existing session/login, redirect
  to login with `returnTo` if absent — mirrors the frontend 401 convention); shows a **consent**
  screen ("Grant <client> access to MCP — read-only / read-write"); validates `client_id`,
  `redirect_uri`, `code_challenge` (PKCE required), `resource`, `scope`; issues an auth code bound
  to all of them.
- **`/token`**: exchanges code + `code_verifier` for an access token (existing JWT format, `aud` =
  MCP resource, scopes = consented, short TTL) + refresh token (rotating). Reuse the auth
  service's signing.
- **`jwks_uri`**: publish the JWT verification key(s) (decision: reuse the existing JWT signing
  key vs introduce JWKS).
- Persist clients, auth codes, and refresh grants (new tables; codes single-use + short-lived).

### Phase 3 — Dynamic Client Registration (RFC 7591)

- `registration_endpoint` accepting `redirect_uris`, `client_name`, etc.; returns
  `client_id` (+ optional secret for confidential clients). Native MCP clients are **public**
  clients using PKCE + **loopback** redirects (`http://127.0.0.1:*` / `http://localhost:*`) — the
  redirect-URI validator must allow the loopback exception while rejecting everything else.

## Security considerations

- **PKCE `S256` mandatory**; reject missing/`plain` challenges.
- **Redirect-URI allow-listing** with the loopback exception for native clients; exact-match for
  registered web clients.
- **Audience binding** (RFC 8707) so MCP tokens can't be replayed at other SolidPing surfaces.
- **Consent** on every new client/scope grant; show requested scope clearly.
- **Short-lived access tokens**, **rotating refresh tokens**, revocation on logout/PAT-revoke.
- Single-use, short-TTL authorization codes; bind code → client → redirect → challenge → resource.
- Rate-limit `/authorize`, `/token`, `/register` (existing rate-limit middleware).

## Decisions (applied 2026-06-20)

1. **Embedded authorization server, not delegation.** SolidPing is self-hosted-first and must work
   with no external IdP, so SolidPing itself is the OAuth 2.1 AS for the MCP resource: reuse the
   existing user login/session for `/authorize` and the existing JWT + `Claims.Scopes` machinery to
   mint access tokens. Delegating to an org's external IdP stays a later option (Out of scope).
2. **DCR is in scope, delivered as Phase 3.** The goal is that standard MCP clients connect with no
   hand-pasted token, which requires RFC 7591 for Claude Desktop / `mcp-remote`. It is the heaviest
   lift, so it ships last in the phasing — but it is not dropped. (Without it we would be limited to
   pre-registered / first-party clients, which fails the stated goal.)

## Remaining open questions (no prior recommendation — still a human call)

These were left genuinely open by the original spec and are **not** resolved here; each needs a
deliberate call before/while building Phase 2–3. A current leaning is noted, but not committed:

3. **Self-hosted only, or SaaS too?** *Lean:* target **self-hosted, single issuer** for v1 (issuer =
   SolidPing's own URL; org taken from the logged-in user's session at `/authorize`, since the MCP
   handler already scopes to `claims.OrgSlug`). The design must not *preclude* SaaS multi-tenant
   `client_id` isolation and admin-gated DCR, but that is a follow-on, not v1.
4. **Reuse the existing JWT signing key, or introduce a JWKS keyset?** *Lean:* **reuse** the
   existing signing key for v1 and publish it at `jwks_uri`; a dedicated keyset is a later hardening
   step.
5. **Which client do we validate against first** — claude.ai remote connector, Claude Desktop, or
   the `mcp-remote` bridge? *Lean:* **Claude Desktop / the claude.ai remote connector** (with
   `mcp-remote` for local testing) — they exercise the hardest constraints (DCR, loopback redirects,
   metadata probes) the spec must satisfy on day one.
6. **Per-org issuer vs single issuer** — tied to (3); the leaning above is a **single issuer** with
   the org selected from the logged-in user's session at `/authorize`.

## Verification

- **Discovery:** the two `.well-known` docs validate (e.g. against the MCP Inspector / an OAuth
  metadata validator); an unauthenticated MCP request returns 401 with a correct
  `WWW-Authenticate: resource_metadata` pointer.
- **End-to-end:** a real MCP client (target chosen in Q5) completes
  discover → register (if DCR) → authorize+consent → token → call a tool, with **no pasted
  token**; a `mcp:read` consent is refused on a mutating tool
  ([`mcp/handler.go:276-280`](../../server/internal/mcp/handler.go)).
- **Security tests (table-driven, `testify/require`, `t.Parallel()`):** missing PKCE rejected;
  reused/expired auth code rejected; wrong/blocked `redirect_uri` rejected; token with wrong
  `aud` rejected at the MCP endpoint; refresh rotation invalidates the prior refresh token.
- **Back-compat:** an `mcp`-scoped PAT pasted as a bearer token still works.
- `make lint` / `make test`; `make test-dash` for the consent UI.

## Files referenced

- `server/internal/app/server.go` — MCP mount (`:486-487`); new `.well-known` + `/authorize` /
  `/token` / `/register` routes (public + session-gated)
- `server/internal/mcp/handler.go` — 401 `WWW-Authenticate`, audience validation
- `server/internal/mcp/scope.go` — `mcp` / `mcp:read` scope mapping (reused for consent)
- `server/internal/handlers/auth/service.go` — JWT/claims/scope issuance to reuse for tokens
- `server/internal/middleware/auth.go` — `RequireAuth`, claims extraction
- New: OAuth AS package (metadata, authorize, token, DCR) + tables (clients, auth codes, refresh)
- `wiki/api-specification.md` — document the OAuth endpoints + discovery
- `wiki/competitors/maintenant.md` — source of the requirement

## Implementation Plan

Decomposed by the three phases. Issuer = `cfg.Server.BaseURL` (single issuer, v1).
MCP resource = `<issuer>/api/v1/mcp`. Reuses the existing HS256 JWT signing key
(`cfg.Auth.JWTSecret`) for access tokens and publishes it at `jwks_uri` (symmetric
key → published as an `oct` JWK; see note in Phase 2). Org is taken from the
logged-in user's session at `/authorize`.

### Phase 1 — Resource Server discovery + audience validation
1. New package `internal/oauth` with metadata builders (issuer, resource, endpoints).
2. Root-level public handler `GET /.well-known/oauth-protected-resource` (RFC 9728)
   advertising `resource` = MCP URL, `authorization_servers` = [issuer].
3. MCP handler: on missing/invalid auth return `401` +
   `WWW-Authenticate: Bearer resource_metadata="<issuer>/.well-known/oauth-protected-resource"`.
   This requires routing the MCP endpoint through an OAuth-aware auth wrapper that
   sets the header on 401 (the generic `RequireAuth` 401 stays JSON for other routes).
4. Audience check: access tokens that carry an `aud` claim must include the MCP
   resource; PATs (no `aud`) keep passing via the existing scope check (back-compat).
   Add `Audience` to `auth.Claims` (jwt `aud`) and an MCP-side audience guard.
5. Tests: well-known doc well-formed; 401 + WWW-Authenticate pointer; aud accept/reject;
   PAT back-compat.

### Phase 2 — Embedded authorization server
1. Migration `002_*.up/.down.sql` (BOTH dialects): `oauth_clients`, `oauth_auth_codes`,
   `oauth_refresh_tokens`. Codes single-use + short-lived; refresh tokens rotating.
2. Models (`oauth_client.go`, `oauth_auth_code.go`, `oauth_refresh_token.go`) + repo
   methods on both `postgres` and `sqlite` Services + `db.Service` interface entries.
3. `GET /.well-known/oauth-authorization-server` (RFC 8414) + alias
   `GET /.well-known/openid-configuration`: authorization_endpoint, token_endpoint,
   registration_endpoint, jwks_uri, `code_challenge_methods_supported=["S256"]`,
   `grant_types_supported=["authorization_code","refresh_token"]`,
   `scopes_supported=["mcp","mcp:read"]`, `token_endpoint_auth_methods_supported=["none"]`.
4. `GET /jwks_uri` (`/.well-known/jwks.json`): publish the JWT verification key.
5. `GET/POST /api/v1/oauth/authorize`: session-gated (reuse JWT/cookie; redirect to
   dash0 login with returnTo when absent). Validate `client_id`, `redirect_uri`
   (allow-list + loopback exception), `response_type=code`, PKCE `code_challenge`
   (required) + `code_challenge_method=S256` (reject missing/plain), `resource`,
   `scope ⊆ {mcp,mcp:read}`. Renders a consent screen (dash0 route). On approve,
   mint single-use auth code bound to client→redirect→challenge→resource→scope→user/org.
6. `POST /api/v1/oauth/token`: `grant_type=authorization_code` (code + code_verifier,
   PKCE S256 verify, single-use, redirect/client/resource match) and
   `grant_type=refresh_token` (rotating: old refresh invalidated, new issued).
   Issues HS256 JWT access token with `aud`=resource, scopes=consented, short TTL.
7. Consent UI: dash0 route `/orgs/$org/oauth/consent` built from design-reference
   primitives; honors 401→login?returnTo.
8. Revocation: logout + PAT-revoke clears related refresh grants.
9. Rate-limit: authorize/token/register sit under `/api/v1/` so the existing per-IP
   limiter covers them.

### Phase 3 — Dynamic Client Registration (RFC 7591)
1. `POST /api/v1/oauth/register` (public): accept `redirect_uris`, `client_name`,
   `grant_types`, `response_types`, `token_endpoint_auth_method`. Native/public
   clients (PKCE + loopback) get no secret; confidential clients get a secret.
   Redirect-URI validator: loopback exception (`http://127.0.0.1:*`, `http://localhost:*`,
   `http://[::1]:*`) else exact-match https (or http loopback). Returns `client_id`
   (+ `client_secret` for confidential), echoes metadata.
2. Tests: loopback accepted, non-loopback http rejected, public client gets no secret.

### Security (throughout)
PKCE S256 mandatory; redirect-URI allow-list + loopback; audience binding; consent per
new grant; short-lived access tokens; rotating refresh + revocation on logout/PAT-revoke;
single-use short-TTL codes bound to client/redirect/challenge/resource; rate limiting.

### Docs
Document discovery + `/authorize` `/token` `/register` in `wiki/api-specification.md`.
