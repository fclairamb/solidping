---
model: opus
effort: high
---

# MCP OAuth tokens, even `mcp:read`, work on the whole REST API and the consent screen, and can mint non-expiring PATs

## Problem
Found by the security scan of 2026-10-11 (finding H3), plus the OAuth consent `session()` variant listed under "Variants of already-known findings" in the 2026-10-12 report. **High.**
- **Who:** any OAuth client a user has consented to, and anyone who steals its token. Every deployment mode.
- **Why it reaches everyone:** dynamic client registration is open and client IDs are server-generated (`server/internal/oauth/service.go:676-700`). Any third-party app can ask a user for read-only `mcp:read` and then receive their full REST role.

**Root cause.** The MCP access token is a bearer JWT on the same key as the dashboard session.
- It is minted by `GenerateMCPAccessToken` (`server/internal/handlers/auth/service.go:2268-2297`, called from `server/internal/oauth/service.go:553`).
- It carries `aud` = the MCP resource and the consented `scopes`.
- Only two places look at either claim:
  - `RequireMCPAuth` checks `aud` (`server/internal/middleware/auth.go:262-266`, via `oauth.TokenHasResourceAudience`, `server/internal/oauth/audience.go:13-32`);
  - the MCP handler checks scopes (`server/internal/mcp/scope.go:17-50`, `mcp/handler.go:320`, `:495`).
- `RequireAuth` (`middleware/auth.go:55-139`) and `ValidateToken` (`service.go:1483-1507`) ignore both. So does every other bearer surface that calls `ValidateToken` directly:
  - the realtime WS handshake, which takes a bearer subprotocol (`server/internal/handlers/realtimews/handshake.go:70-128`);
  - feedback attribution (`server/internal/handlers/feedback/handler.go:121-160`).

**What a token with `mcp:read` consent can do on REST:**
- **Every REST call and write the user's role allows.** `RequireOrgWrite` and `requireOrgRole` look at the membership row, not the token (`middleware/auth.go:693-770`). An MCP token has an empty `Role` and the `OrgSlug` of the grant, so it passes `AuthorizeOrgAccess`.
- **Mint a PAT:** `POST /orgs/:org/tokens` (`server/internal/app/server.go:924-926`, `CreatePAT` at `service.go:1948-2025`).
  - The PAT gets the user's full role: the MCP scopes are not copied.
  - Its lifetime is whatever the caller sends: `token.ExpiresAt = req.ExpiresAt` (`service.go:2000`). Omitting `expiresAt` gives a PAT that never expires (`ValidatePATToken` treats nil as no expiry, `:1542`).
  - The PAT outlives revoking the grant: revoking a PAT revokes the user's grants (`:2058-2066`), not the other way round.
- **Mint a dashboard session:** `POST /auth/switch-org` (`server.go:787`, `SwitchOrg` at `service.go:2086-2168`) turns the token into a full session with a refresh token.

**This is pinned as intended.** `TestCLIBrowserLoginWalkthrough` (`server/internal/app/cli_oauth_login_test.go:24-139`, assertion at `:100-115`) relies on it for the loopback CLI login of spec 2026-07-11-08: the OAuth access token mints a PAT. Nothing limits that to the CLI client.

**The consent variant.** `session()` (`server/internal/oauth/authorize.go:123-143`) authenticates both `GET /oauth/authorize` (`:147-165`) and the approve `POST` (`:172-217`) through plain `ValidateToken`. Because this endpoint sits outside `RequireAuth`, a fix in the middleware alone would not cover it.
- It accepts a PAT, and an MCP access token. Only impersonation is refused (`:134-138`).
- `ApproveAuthorize` takes the scope from the POST body (`parseAuthRequest`, `:30-46`). So an `mcp:read` credential can approve a full `mcp` grant, for any registered client, without any human consenting.

**The CLI today.** The in-tree `sp auth login` no longer uses the loopback flow: since spec 2026-08-08-02 it uses the device flow, which returns a PAT minted server-side (`server/pkg/cli/auth_device.go:20-49`, `server/internal/oauth/cli.go:5-12`). It never presents an MCP token to REST, and neither do `sp auth logout` (it calls `/oauth/revoke` without a bearer, `server/pkg/cli/apihelper/apihelper.go:548-575`) or `sp mcp`. Only CLI binaries released between those two specs, and any other native client using the seeded `solidping-cli` registration, still mint their PAT with the OAuth access token.

## Proposal
1. **One predicate.** Add `func (c *Claims) IsMCPCredential() bool` in `server/internal/handlers/auth/service.go`, next to `IsImpersonation` (`:237-241`).
   - It returns `true` when `len(c.Audience) > 0`. Only OAuth resource tokens carry `aud`, and every MCP token minted so far has one, so it works on tokens already issued.
   - If Open question 1 is answered yes, also return `true` when `len(c.Scopes) > 0`.
   - When spec 2026-10-16-02 lands first, nothing changes: this keys on `aud`, not on its signing keys.
2. **Put the client in the token.** Add `ClientID string \`json:"clientId,omitempty"\`` to `Claims` (`service.go:199-235`). Pass `mintInput.clientID` (`oauth/service.go:499-512`) into `GenerateMCPAccessToken` as a new parameter, and update its callers and the tests that call it (`middleware/mcpauth_test.go`, `oauth/revoke_test.go`).
3. **Refuse MCP credentials on REST.** In `RequireAuth`, right after `ValidateToken` (`middleware/auth.go:71-75`), answer **401 `INVALID_TOKEN`** with "This token is only valid for the MCP endpoint" when `claims.IsMCPCredential()`, unless `auth.IsMCPCredentialRouteAllowed(claims, req.Method, httpx.RoutePattern(req))` says otherwise. This mirrors `RequireMCPAuth`'s wrong-audience 401. Put the allow-list in `server/internal/handlers/auth/`, built like `impersonationForbiddenRoutes` (`impersonation_guard.go:41-103`) and reusing its pattern constants (`:12-26`):

   | Method | Route pattern | Allowed for |
   |---|---|---|
   | `DELETE` | `/api/v1/auth/tokens/current` (`ImpersonationPathTokenCurrent`) | any MCP credential: dropping your own grant is never an escalation (spec 2026-07-18-05) |
   | `POST` | `/api/v1/orgs/{org}/tokens` (`ImpersonationPatternOrgTokens`) | only `ClientID == oauth.CLIClientID` with the full `mcp` scope: the legacy loopback CLI |

   - `GET /api/v1/auth/tokens` is deliberately not on the list. Spec 2026-07-18-05 imagined an agent listing tokens to find its own `isCurrent` grant, but no first-party MCP client calls it, and `DELETE /tokens/current` needs no listing.
   - `RequireMCPAuth` is unchanged, so MCP tokens keep working at `/mcp`.
   - Dynamic client IDs are random, so no third party can claim `solidping-cli` (`oauth/service.go:679`). Its only registered redirect is loopback (`oauth/cli.go:23-26`).
4. **The other `ValidateToken` callers.**
   - `realtimews` `authenticateToken` (`handshake.go:100-128`): refuse an MCP credential with the same 401.
   - `feedback.attachUserIfAuthenticated` (`handler.go:121-160`): treat it as anonymous.
5. **Consent accepts dashboard sessions only.** In `session()` (`oauth/authorize.go:123-143`), return "no session" for:
   - a PAT (`strings.HasPrefix(token, auth.PATTokenPrefix)`, the constant at `service.go:1480`);
   - any `claims.IsMCPCredential()`.
   - Keep the impersonation refusal.
   - `Authorize` then bounces these callers to login, and `ApproveAuthorize` answers 401.
   - Once only a logged-in human can reach it, taking the scope from the POST body is the normal consent flow: the body is what the consent screen showed, re-validated by `validateAuthRequest` (`:54-118`). It needs no separate fix.
6. **Docs and changelog.**
   - `web/docs/docs/features/mcp.md:228`: say that OAuth tokens (and scoped PATs, if Open question 1 is yes) only work on `/api/v1/mcp`.
   - `wiki/api-specification/auth.md:118`: note that `DELETE /tokens/current` accepts an MCP token.
   - A changelog entry, since a script that used an MCP token on REST stops working.

**How the CLI keeps working.**
- `sp auth login` (device flow), `sp auth logout` (`/oauth/revoke`, no bearer) and `sp mcp` (PAT on `/mcp`) never present an MCP token to REST, so nothing changes for them.
- A legacy loopback CLI keeps working through the `POST /orgs/{org}/tokens` exception. It mints its PAT right after the token exchange, so a token minted before the deploy (no `clientId` claim) is never in play.
- `TestCLIBrowserLoginWalkthrough` stays green. Only its comments change: "RequireAuth ignores scope and audience" becomes "the CLI client exception".

## Tests
- **`server/internal/middleware/mcp_credential_rest_test.go`** (new). Use a real router as `impersonation_test.go:74-160` does, so `httpx.RoutePattern` resolves. Mint MCP tokens with `GenerateMCPAccessToken` for client IDs `dyn-123` and `solidping-cli`. Table-driven:
  - **Regression cases** (each one passes today, so the test fails before the fix):
    - `mcp:read`, dynamic client, `GET /api/v1/orgs/{org}/checks`: 401 `INVALID_TOKEN` (200 today);
    - `mcp`, dynamic client, `POST /api/v1/orgs/{org}/tokens`: 401 (201 today);
    - `mcp:read`, dynamic client, `POST /api/v1/auth/switch-org`: 401 (200 today);
    - `solidping-cli` with `mcp:read`, `POST /orgs/{org}/tokens`: 401;
    - `solidping-cli` with `mcp`, `GET /orgs/{org}/checks`: 401.
  - **Allowed:**
    - any MCP token on `DELETE /api/v1/auth/tokens/current`: passes the gate;
    - `solidping-cli` with `mcp` on `POST /orgs/{org}/tokens`: passes.
  - **Positive controls:**
    - a dashboard session token and an unscoped PAT pass on every route above;
    - the same MCP tokens still pass `RequireMCPAuth` (existing `mcpauth_test.go:110` stays green).
- **`server/internal/oauth/authorize_session_test.go`** (new, `setupOAuthHandler`, `handler_test.go:19`). Approve `POST` with `scope=mcp`:
  - a PAT as bearer: 401, no code minted;
  - an `mcp:read` MCP access token as bearer: 401, no code minted;
  - an impersonation token: 401 (existing behaviour);
  - positive control: a dashboard session: 302 to the redirect URI with a code. `GET /authorize` with a PAT: 302 to `/d/login`.
- **`server/internal/app/cli_oauth_login_test.go`:**
  - `TestCLIBrowserLoginWalkthrough` stays green;
  - add a twin that runs the same walkthrough with a dynamically registered client (as `mcp_endpoint_test.go:310-330` does) and asserts the PAT mint answers 401.
- **`server/internal/app/mcp_endpoint_test.go` `TestMCPClientWalkthroughOAuth`** (`:276`): after the MCP round trip, the same `tok.AccessToken` on `GET /api/v1/orgs/mcp-e2e/checks` answers 401.
- **Route table** (`server/internal/app/`, like `TestImpersonationDenylistNamesRealRoutes`, `impersonation_test.go:269`): every allow-list entry names a registered route.
- **Realtime:** a test in `server/internal/handlers/realtimews/` where an MCP token on the handshake gets 401 and a session token is accepted.
- **`server/internal/oauth/revoke_test.go` `TestMintedAccessTokenCarriesGrantUID`** (`:171`): also assert the `clientId` claim.

## To verify
- Which REST routes legacy CLI binaries (spec 2026-07-11-08) call with the OAuth access token before they switch to the PAT. The spec and today's test show only `POST /orgs/{org}/tokens`. If an older build also called `GET /auth/me` with it, add that route to the CLI exception.
- `GET /api/v1/auth/tokens` and `isCurrent` for OAuth grants (`token_iscurrent_test.go`) are only exercised with session principals, so excluding MCP tokens from that route breaks no test.

## Open questions
1. **Scoped PATs on REST.** A PAT created with `scopes: ["mcp:read"]` (documented in `web/docs/docs/features/mcp.md:228`) is also accepted on REST with the user's full role. This is the "PAT side of H3" in the 2026-10-12 variants. Extend `IsMCPCredential` to `len(Scopes) > 0`, so a scoped credential works only where its scope means something?
   - Recommended: yes. It is the same one-line gate, and no first-party client creates scoped PATs. A user who scoped a PAT to `mcp:read` expects it to be read-only.
2. **Remove the CLI exception later?** The in-tree CLI no longer needs it.
   - Recommended: keep it for two releases, then delete it along with the PAT step of `TestCLIBrowserLoginWalkthrough`. Add a `TODO(remove after …)` on the allow-list entry, as `findInvite` does (`service.go:3805`).

## Related
- **M7 (2026-10-11), not covered:** removed members keep MCP read access; `RequireMCPAuth`, consent, code exchange and refresh never re-check membership.
- **T8 (2026-10-13), not covered:** password reset, password change and email change leave OAuth refresh grants alive.
- **C2 and H1 (2026-10-11):** the same "one key, no token type" design, covered by spec 2026-10-16-02.
