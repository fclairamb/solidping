# `sp auth login` only supports email+password in the terminal — no browser OAuth path for agents and CLI users

## Problem

The CLI logs in with an email and password typed into the terminal:
`authLoginAction` (`server/pkg/cli/auth.go:21`) takes `--email`/`--password`
flags or interactive prompts, calls `POST /api/v1/auth/login`, and saves the
resulting session JWT to `~/.config/solidping/token.json`
(`server/pkg/cli/config/config.go:51`, `TokenPath`). This has several problems:

- Passwords land in shell history (flags) or get typed into whatever
  terminal/agent is driving the CLI — the worst possible credential handoff
  for a bot or coding agent that should never see the human's password.
- Users who authenticate via an OAuth/SSO provider have no password to type,
  so they cannot use the CLI at all.
- The saved credential is a short-lived session JWT; getting a durable
  credential requires a second manual step (`sp tokens create`,
  `server/pkg/cli/tokens.go:118`, or the dashboard tokens page
  `web/dash0/src/routes/orgs/$org/account.tokens.tsx`).

Meanwhile the server already ships everything needed for the standard
"CLI opens a browser, human approves, machine gets a credential" flow, because
MCP clients use it today:

- OAuth 2.1 authorization server in `server/internal/oauth`, wired at
  `server/internal/app/server.go:592-601` (`/api/v1/oauth/authorize` GET/POST,
  `/token`, `/register`, plus `.well-known` metadata).
- Authorization code + PKCE with RFC 8252 §7.3 loopback redirects —
  `http://127.0.0.1:<any-port>/…` is accepted with port-agnostic matching
  (`server/internal/oauth/client.go:10-15`, `authorize.go:67`), built
  explicitly for native clients that "spin up an ephemeral loopback listener".
- A consent page in the dashboard
  (`web/dash0/src/routes/orgs/$org/oauth.consent.tsx`).
- Org-scoped PATs with optional scopes, created via
  `POST /api/v1/orgs/:org/tokens` (`Handler.CreateToken`
  `server/internal/handlers/auth/handler.go:296`, `Service.CreatePAT`
  `server/internal/handlers/auth/service.go:1635`).

The CLI simply doesn't use any of it. Closing that gap gives both humans and
agents a safe default onboarding: the human approves once in a browser, the
machine ends up holding only a named, individually-revocable PAT.

## Proposal

Make browser login the default `sp auth login` behavior, keeping
email/password as an explicit non-interactive fallback.

1. **Pre-registered public client.** Seed a well-known `solidping-cli` client
   in `oauth_clients` (`server/internal/db/models/oauth.go:15`; lookup is
   `GetOAuthClientByClientID`, `server/internal/oauth/service.go:104`) at
   server startup, idempotently: public client (no secret), grant type
   `authorization_code`, loopback redirect URI. The existing loopback
   validation already ignores the port, so one registered
   `http://127.0.0.1/callback` URI covers every ephemeral port. No dynamic
   client registration needed for the CLI.

2. **CLI browser flow** (`server/pkg/cli/auth.go`):
   - Bind an ephemeral listener on `127.0.0.1:0`.
   - Generate a PKCE verifier/challenge and a random `state`.
   - Open the system browser at `/api/v1/oauth/authorize?client_id=solidping-cli&…`
     (print the URL as a fallback when no browser can be opened).
   - The human logs in and approves on the existing consent page; the
     loopback callback receives the one-time authorization code, the CLI
     verifies `state` and serves a minimal "you can close this tab" page.
   - Exchange the code at `/api/v1/oauth/token`
     (`ExchangeAuthCode`, `server/internal/oauth/service.go:200`; grants are
     always org-scoped, `service.go:307`).
   - With the resulting access token, immediately call
     `POST /api/v1/orgs/:org/tokens` to self-mint a named PAT
     (e.g. `sp CLI on <hostname>`), then discard the OAuth access/refresh
     tokens. Only the one-time code ever transits the loopback redirect —
     never a long-lived credential in a URL.
   - Save the PAT via the existing config path (`TokenPath`); `sp auth me`
     should report `"auth_method":"pat"`.

3. **Fallbacks preserved.** `--email`/`--password` keeps working exactly as
   today (non-interactive scripts, self-hosted without SSO). Additionally a
   `--token` flag (or `SP_TOKEN` env) should accept a pasted PAT for headless
   machines where the browser runs elsewhere — the human creates the PAT on
   the dashboard tokens page and pastes it.

4. **Server-side surface.** No new endpoints. The only server change is the
   idempotent seeding of the `solidping-cli` client (plus tests). Note that
   `RequireAuth` (`server/internal/middleware/auth.go:48-84`) does not enforce
   scopes or audience, so the exchanged OAuth access token can call the PAT
   endpoint — this is a pre-existing property, relied on here, worth an
   explicit test.

5. **Tests.**
   - Unit tests for the CLI's PKCE/state/loopback-callback handling.
   - Integration test walking authorize → consent (form POST) → code →
     `/token` → `POST /orgs/:org/tokens` against the test server; the OAuth
     fixtures already exercise loopback redirects
     (`server/internal/oauth/service_test.go:68`).
   - A test asserting the seeded client exists after startup and that
     re-seeding is a no-op.

Open questions:

- **Requested scope.** Existing scopes are `mcp` / `mcp:read`
  (`server/internal/mcp/scope.go:12-13`). Since `RequireAuth` ignores scopes,
  any scope works for the PAT-minting call; a dedicated `cli` scope would be
  semantically cleaner but adds enforcement questions. Lean: reuse the default
  scope for v1 and keep scope semantics out of this spec.
- **Org selection.** OAuth grants are org-scoped and the consent page lives
  under `/orgs/$org/…` — confirm the authorize funnel lets a multi-org user
  pick which org to grant, and have the CLI derive the org from the token
  response (or `/auth/me`) rather than asking again.
- **PAT expiry and scope of the minted PAT.** Match the dashboard default for
  expiry; lean toward an unscoped (full-access) PAT, consistent with
  `sp tokens create`, rather than restricting to `mcp`.
- **Registration in the funnel.** If the human has no account yet, the
  authorize page currently dead-ends at login. Making registration reachable
  from the OAuth login page is deliberately a separate spec.

## Implementation Plan

### Server side

1. **Well-known client constants** — `server/internal/oauth/cli.go`: exported
   `CLIClientID = "solidping-cli"` and `CLIRedirectURI = "http://127.0.0.1/callback"`
   (loopback; port ignored by `RedirectURIAllowed`). Shared by the seed and the
   CLI so the two never drift.

2. **Idempotent seed** — `server/internal/app/cli_oauth_seed.go`:
   `(*Server).SeedCLIOAuthClient(ctx)` looks up the client by `CLIClientID`; if
   present it is a no-op, otherwise it inserts a public client (no secret),
   grant types `authorization_code`+`refresh_token`, scope `mcp`, redirect
   `CLIRedirectURI`. `client_id` already has a unique index, so a lost insert
   race is treated as success. Wired in `server/main.go` after migrations,
   alongside the other seeds.

### CLI side

3. **PAT storage** — `apihelper.go`: add `TokenData.PAT` (opaque `pat_…`,
   never expires client-side), a `SavePAT` writer, a `resolveToken` priority
   for it (returned verbatim), and `AuthMethod(ctx)` returning `"pat"` when the
   resolved credential starts with `pat_`, else `"jwt"`.

4. **Browser flow** — `server/pkg/cli/auth_browser.go`: bind `127.0.0.1:0`,
   generate PKCE verifier/challenge (S256) + random `state`, open the system
   browser at `<url>/api/v1/oauth/authorize?client_id=solidping-cli&…` (print
   the URL as fallback), serve the loopback `/callback` (verify `state`, render
   a close-tab page), exchange the code at `/api/v1/oauth/token`, derive the org
   from the access-token JWT, self-mint a named PAT (`sp CLI on <hostname>`,
   90-day expiry to match the dashboard default, unscoped) via
   `POST /orgs/:org/tokens`, discard the OAuth tokens, and save only the PAT.
   The callback handler is factored into a testable struct.

5. **Login dispatch** — `auth.go` `authLoginAction`: `--token`/`SP_TOKEN` →
   save the pasted PAT; else `--email`/`--password` (or env) → existing password
   login (prompt for a missing password); else → browser flow (new default).
   `authMeAction` reports `AuthMethod(ctx)` instead of the hard-coded `"jwt"`.
   `commands.go`: add the `--token` flag to `auth login`.

### Tests

6. Seed unit test (client exists after seed; re-seed is a no-op).
7. CLI unit tests for PKCE/state/loopback-callback handling.
8. Full-server integration test: login → authorize → consent POST → code →
   `/token` → `POST /orgs/:org/tokens` (asserts the OAuth access token can mint
   a PAT through `RequireAuth`) → PAT works.
