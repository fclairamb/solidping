---
model: opus
effort: high
---

# Federated logins put the session tokens in the URL

## Problem

After a successful provider callback, the backend redirects the browser to a
dashboard URL carrying `access_token`, `refresh_token` and `expires_in` in the
query string. Handlers involved:

- `buildSuccessRedirect` in `server/internal/handlers/auth/{google,github,gitlab,microsoft,discord,oidc,saml}.go`
- `pendingMembershipRedirect` in `join_policy.go` (org-less session, access
  token only)
- `slack.go` still sets `access_token` on one redirect, even though
  `auth.slack.complete.tsx` already goes through `POST /api/v1/auth/slack/exchange`
  with a one-time `code`

A credential in a URL ends up in:

- session replay network capture (spec `2026-09-25-11`, confirmed in prod)
- browser history and sync
- the navigation `Referer` of any request made before `main.tsx` strips it
- any reverse proxy or access log that records the request line
- screenshots and shared links while the page is still loading

Redacting each of these one by one does not scale. The token should never be
in the URL.

## Proposal

Generalize the Slack pattern to every federated login:

1. On callback success, the backend stores the minted session under a random
   single-use **handoff code**: 32 bytes, base64url, expires after 60 s, bound
   to the user/org it was minted for. It then redirects to
   `/d/auth/complete?code=<code>` (the org-less case adds
   `&membershipPending=<slug>`, which is not a secret).
2. `POST /api/v1/auth/handoff/exchange` with `{ "code": "…" }` returns the
   usual login-shaped response (`accessToken`, `refreshToken`, `expiresIn`,
   `user`, `organization`, `organizations`, `loginAction`). The code is deleted
   on first use. A second use, an expired code or an unknown code all answer
   `401 UNAUTHORIZED` with the same body, so the answer leaks nothing.
3. Storage: a small `auth_handoff_codes` table (hash of the code, payload,
   `expires_at`) on both Postgres and SQLite, swept by an existing sweep job.
   Store only the SHA-256 of the code. Reuse Slack's storage if it already
   fits; otherwise move Slack onto the new one so there is a single mechanism.
4. dash0: a new `auth.complete` route exchanges the code, feeds the response
   through `applyLoginResponse` (so forced password rotation, `loginAction`
   and the org-less case behave exactly like password login), then navigates
   with `resolveDestination`.
5. Remove the query-token path: `parseOAuthHandoff` in `oauth-handoff.ts`, the
   IIFE in `main.tsx`, and the defensive effect in `$org.tsx`
   (`hasOAuthTokenInURL`). Keep accepting the old query params for one release,
   so a login started just before the deploy still completes. Mark the
   fallback `// TODO(remove after next release)` with this spec's name.
6. `docs`: update any page describing the SSO redirect. Add a changelog entry.

Out of scope: the OAuth *authorization server* (`internal/oauth`, MCP
clients). It already uses proper authorization codes.

## Tests

- Backend, table-driven, both databases: exchange succeeds once; the second use
  gives 401; an expired code gives 401; an unknown code gives 401; the three
  401 bodies are byte-identical; the stored row holds the hash, not the code.
- One callback test per provider handler asserts the `Location` header contains
  `code=` and does NOT contain `access_token`, `refresh_token` or `expires_in`.
  Include the org-less `pendingMembershipRedirect` case.
- dash0 Playwright: the existing SSO/fake-provider E2E flow lands on the
  dashboard authenticated, and `page.url()` never contains `access_token`
  (check every `framenavigated`).
- Legacy fallback: a URL with `access_token=` still logs in during the
  deprecation window.

## Implementation Plan

### Storage (`auth_handoff_codes`, new section of the unreleased 024 migration)

- New `-- SECTION: auth-handoff-codes` in `024_v0_33_0.{up,down}.sql`, both
  engines: `code_hash` (PK, hex SHA-256 of the code), `user_uid` and nullable
  `organization_uid` (FK, cascade: the binding to the user/org it was minted
  for), `payload` (sealed text), `expires_at`, `created_at`, index on
  `expires_at`.
- `models.AuthHandoffCode`, and three `db.Service` methods on both engines:
  `CreateAuthHandoffCode`, `ConsumeAuthHandoffCode(codeHash)` (one
  `DELETE … RETURNING`, so single use is atomic: two concurrent exchanges
  cannot both win) and `DeleteExpiredAuthHandoffCodes(before)`.
- The payload holds live tokens for up to 60 s, so it is not stored in clear
  either: AES-256-GCM under a key derived from the code (HKDF-SHA256, distinct
  from the lookup hash), with the code hash as associated data. A database
  dump alone yields neither the code nor the tokens. This keeps the new table
  in line with spec 2026-09-25-23 (refresh tokens hashed at rest).
- Swept by the existing state-cleanup job.

### Package `internal/authhandoff`

`Issue(ctx, db, Session, ttl) (code, error)` and `Redeem(ctx, db, code)
(*Session, error)`. Code = 32 random bytes, base64url. `Redeem` returns one
error (`ErrInvalidCode`) for unknown, used, expired, forged or undecryptable
codes. The code is never passed to a logger.

### Backend handlers

- One shared tail, `finishProviderCallback`, used by google, github, gitlab,
  microsoft, discord, slack (sign-in), oidc and saml. It sets the SPA cookie as
  today, issues a handoff code for the minted session (org-scoped or
  org-less), and redirects to `/d/auth/complete?code=<code>`, plus
  `&membershipPending=<slug>` when the login is pending on an org. The eight
  `buildSuccessRedirect` methods go away. The `redirect_uri` from the OAuth
  state is no longer the redirect target: it travels inside the sealed payload
  and comes back from the exchange as `returnTo`, which the dashboard runs
  through `resolveDestination`'s guards. The handoff code therefore only ever
  goes to our own `/d/auth/complete` (relevant to spec 2026-09-25-18).
- `pendingMembershipRedirect` no longer carries `access_token`/`expires_in`.
  `RedirectPendingMembership` becomes `RedirectWithHandoff`, used by the Slack
  app-install callback for both its pending and its admitted outcome.
- Slack app install moves onto the same mechanism (`IssueExchangeCode` and the
  `slack-exchange` oauthstate kind are replaced). The channel to land on is
  sent back as `returnTo` (`/d/orgs/<slug>/integrations/<channelUid>`).
- `POST /api/v1/auth/handoff/exchange {code}` (public): redeems the code,
  loads the user/org exactly like `/auth/me`, and answers the login shape
  (`accessToken`, `refreshToken`, `expiresIn`, `tokenType`, `user`,
  `organization`, `organizations`, `loginAction` = `noOrg` for an org-less
  session) plus `returnTo` / `membershipPending`. Every failure answers the
  same `401 UNAUTHORIZED` body.
- Legacy, one release (`TODO(remove after next release)`): `POST
  /api/v1/auth/slack/exchange` keeps redeeming codes minted into
  `state_entries` by an older pod.

### Dashboard

- New route `auth.complete.tsx` (`/auth/complete`): strips `code` from the URL
  first, exchanges it once (deduplicated across StrictMode double effects),
  feeds the response to `applyLoginResponse`, then navigates: org session →
  `resolveDestination(org, returnTo)`; org-less → `/no-org` with
  `membershipPending`. A failed exchange shows an error with a link back to
  sign-in. The landing decision is a pure, unit-tested function.
- Remove the defensive `hasOAuthTokenInURL` effect and gates in `$org.tsx`.
- Keep `parseOAuthHandoff` + the `main.tsx` IIFE for one release as the
  legacy fallback (a callback served by an old pod during a rolling deploy),
  marked `TODO(remove after next release)`. `auth.slack.complete.tsx` likewise.
- `analytics-redaction` tests: pin that `/d/auth/complete?code=…` is redacted.

### Docs

`web/docs/docs/configuration/authentication.md` (what happens after the
provider redirect), `wiki/api-specification/auth.md` (new endpoint, legacy
Slack one), `openapi.yaml` (new endpoint). No CHANGELOG edit (batch
convention: the release PR writes it).

### Tests

- `authhandoff`: round trip, second use fails, expired, unknown, forged
  (right hash, tampered payload), stored row holds the hash and no plaintext
  token — SQLite and Postgres.
- `auth` handler: exchange endpoint, table-driven on both engines: success
  once with the same session shape (org and org-less), then 401; expired /
  unknown / reused bodies byte-identical.
- Per-provider callback test (table over google, github, gitlab, microsoft,
  discord, slack, oidc, saml + the pending case): `Location` has `code=` and
  no `access_token` / `refresh_token` / `expires_in`, and the code redeems.
- Slack install callback: admitted and pending redirects carry a code only.
- dash0 unit: landing decision. Playwright: fake OIDC IdP end-to-end, every
  `framenavigated` URL free of `access_token`; legacy `?access_token=` URL
  still signs in; a bogus code shows the error. The OIDC spec self-skips
  unless the server has the fake IdP configured, and CI's side-car server
  gets that configuration.
