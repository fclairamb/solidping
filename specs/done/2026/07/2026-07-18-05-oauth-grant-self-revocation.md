---
model: opus
effort: high
---

# OAuth/MCP clients can't revoke their own grant when their work is done

## Problem

When an MCP client (Claude) or the `sp` CLI connects, the token exchange
persists a rotating OAuth grant — a `user_tokens` row of type `oauth_refresh`
(`mintTokens`, `server/internal/oauth/service.go:339`) with a long
`refreshTokenTTL` (`server/internal/oauth/service.go:30`). Once the agent has
finished doing its work, there is no way for it (or for the user acting through
it) to revoke that grant:

- The OAuth surface only exposes `/authorize`, `/token`, `/register`
  (`server/internal/app/server.go:623-627`). There is no RFC 7009
  `POST /oauth/revoke`, and the authorization-server metadata
  (`server/internal/oauth/metadata.go`) advertises no `revocation_endpoint`.
- MCP access tokens are minted without any link to their grant
  (`GenerateMCPAccessToken` call at `server/internal/oauth/service.go:342`
  takes only user/org/scopes/resource). A caller authenticated with such a
  token has no `claims.RefreshUID`, so it can't tell which row in
  `GET /api/v1/tokens` is its own: `tokenToInfo` only marks `isCurrent` for
  session refresh tokens (`server/internal/handlers/auth/service.go:1534`).
- The existing teardown paths are all blunt or indirect: the human hunting the
  row down via `DELETE /api/v1/tokens/:tokenUid`
  (`server/internal/app/server.go:459`), a PAT revoke's revoke-all-oauth-grants
  side effect (`server/internal/handlers/auth/service.go:1723-1726`), full
  logout with `deleteAllTokens: true`, or waiting for expiry.

Net effect: every finished agent session leaves a live, refreshable credential
lingering until TTL — poor hygiene, and against the principle that a principal
should always be able to drop its own access.

## Proposal

Let the current principal revoke exactly the grant backing its own
credentials.

1. **RFC 7009 revocation endpoint** — `POST /api/v1/oauth/revoke`,
   form-encoded `token` (+ optional `token_type_hint`), same client
   authentication / `client_id` binding rules as `/token`
   (`server/internal/oauth/token.go`). For a refresh token: look up the
   `oauth_refresh` row and soft-delete it (same mechanism as
   `RevokeToken`). Per the RFC, always return 200 — including for unknown,
   already-revoked, or other-client tokens — so the endpoint can't be used to
   probe token validity. Only delete when the row's `client_id` property
   matches the authenticated client and the token is an `oauth_refresh` row.
2. **Advertise it** — add `revocation_endpoint` to the authorization-server
   metadata (`server/internal/oauth/metadata.go` + its test) so compliant
   clients discover it.
3. **Link access tokens to their grant** — create the refresh row first in
   `mintTokens`, then embed its UID in the MCP access-token claims (thread a
   grant UID through `GenerateMCPAccessToken` into `Claims.RefreshUID`,
   `server/internal/handlers/auth/service.go:165`). Rotation
   (`ExchangeRefreshToken`, `server/internal/oauth/service.go:263`) naturally
   re-links each new access token to the new row. This enables:
   - `GET /api/v1/tokens` marking the caller's own OAuth grant `isCurrent`
     (extend `server/internal/handlers/auth/service.go:1534` beyond session
     refresh tokens), so both the dashboard and an agent listing tokens can
     identify "the grant I'm riding on".
   - Bearer-only self-revocation: `DELETE /api/v1/tokens/current` (or
     equivalent) that revokes the row named by `claims.RefreshUID` — the
     "I'm done, drop my access" call for a client that no longer holds its
     refresh token, and works for session principals too.
4. **CLI logout** — `sp auth logout` should call the revocation endpoint with
   its stored refresh token before deleting local credentials (the login
   counterpart landed in spec 2026-07-11-08).

### Tests

- Revoking own grant → the refresh token is dead immediately (mirror
  `TestRefreshRevokedTokenFailsImmediately`,
  `server/internal/handlers/auth/session_test.go:126`) and outstanding access
  tokens for that grant can no longer be traded for new ones.
- Negative controls: revoking a token bound to another `client_id` or another
  user returns 200 but deletes nothing; a PAT or session refresh token
  presented to `/oauth/revoke` is not deletable through this path.
- Metadata test asserts `revocation_endpoint` is advertised.
- `isCurrent` shows up on the caller's OAuth grant in the tokens listing.

### Open questions

- Exact shape of the bearer-only self-revocation surface: a dedicated
  `DELETE /tokens/current` vs. making plain `POST /logout` revoke
  `claims.RefreshUID` when it's an OAuth grant (today a body-less logout only
  clears the cookie, `server/internal/handlers/auth/handler.go:184-187`).
- Should scoped-down tokens (`mcp:read`) be allowed to self-revoke? Leaning
  yes — dropping your own credential is never a privilege escalation.

## Implementation Plan

### Decisions on the open questions

1. **Bearer-only self-revocation shape → a dedicated `DELETE` endpoint, not
   overloaded logout.** I add `DELETE /api/v1/auth/tokens/current` (the
   existing token family is mounted under `/api/v1/auth`, so the spec's
   shorthand `/api/v1/tokens/current` becomes `/api/v1/auth/tokens/current`,
   registered as `rootAuthProtected.DELETE("/tokens/current", …)` next to the
   existing `DELETE /tokens/:tokenUid`; bunrouter routes the static segment
   ahead of the `:tokenUid` param, exactly like the existing
   `/passkeys/register/begin` vs `/passkeys/:uid` pair). Rationale: it is
   explicit and discoverable, it does not conflate "drop my access" with
   logout's cookie-clearing/session semantics, and — because `RequireAuth`
   does *not* enforce the MCP audience — it accepts an MCP access token too, so
   a client holding only its short-lived access token can still drop the grant
   it rides on. It also works unchanged for session and dashboard principals.
   The handler simply revokes `claims.RefreshUID` via the existing
   `RevokeToken(userUID, refreshUID)` (which already verifies row ownership);
   a credential with no backing grant row (PAT / 2FA temp token, empty
   `RefreshUID`) gets a 400 rather than a misleading 404.

2. **Scoped-down (`mcp:read`) tokens may self-revoke → yes.** Neither
   `DELETE /tokens/current` nor `POST /oauth/revoke` gates on scope, so a
   `mcp:read` credential can drop its own grant. Dropping your own credential
   is never a privilege escalation, matching the spec's lean.

### Client-authentication model for `/oauth/revoke`

The existing `/token` endpoint authenticates the client purely by `client_id`
binding (no client-secret verification in this codebase — see
`tokenRefresh`/`ExchangeRefreshToken`). `/oauth/revoke` mirrors that: it reads
`token` + optional `token_type_hint` + `client_id` from the form and
**soft-deletes the grant only when it is an `oauth_refresh` row whose
`client_id` property equals the presented (non-empty) `client_id`.** Every
other outcome — unknown token, already-revoked, expired, a PAT or session
refresh token, a grant bound to another `client_id` (hence another
user/client) — is a silent no-op. Per RFC 7009 the endpoint always answers
`200` with an empty body so it can't be used as a token-validity oracle.
(Client-id binding is the enforced boundary; a same-client caller that already
possesses another principal's raw refresh-token secret can revoke it, which is
inherent RFC 7009 possession semantics and not an escalation.)

### Steps

1. **`/oauth/revoke` endpoint** — `metadata.go`: add `PathRevoke`
   (`/api/v1/oauth/revoke`) + `RevocationEndpoint()`. `service.go`: add
   `RevokeGrant(ctx, refreshToken, clientID) (bool, error)` implementing the
   binding rules above. `token.go` (or a new `revoke.go`): add the
   `Revoke` handler (always 200). Register `oauthGroup.POST("/revoke", …)` in
   `server.go`.
2. **Advertise it** — add `revocation_endpoint` to
   `AuthorizationServerMetadata` + `BuildAuthorizationServerMetadata`, and its
   tests.
3. **Link access tokens to their grant** — reorder `mintTokens` to persist the
   refresh row first, then thread its UID through `GenerateMCPAccessToken` into
   `Claims.RefreshUID`; rotation re-links for free (it calls `mintTokens`).
   Extend `tokenToInfo` so an `oauth_refresh` row matching the caller's
   `RefreshUID` is flagged `isCurrent`. Add `DELETE /tokens/current` handler +
   route.
4. **CLI logout** — `Helper.Logout` best-effort POSTs the stored refresh token
   to `/api/v1/oauth/revoke` (`client_id=solidping-cli`) before deleting local
   credentials.
5. **Tests** — own-grant revoke kills the refresh token immediately + a
   later `ExchangeRefreshToken` fails; negative controls (other `client_id` /
   other user → 200 no-op; PAT and session refresh token not deletable via this
   path); metadata advertises `revocation_endpoint`; `isCurrent` on the
   caller's OAuth grant.
