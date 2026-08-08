---
model: opus
effort: high
---

# CLI login should use the OAuth device authorization flow (like dbbat / GitHub)

## Problem

`sp auth login` defaults to a loopback-callback browser OAuth flow
(`server/pkg/cli/auth_browser.go`): the CLI starts a localhost HTTP listener,
opens the browser to the server's own `/api/v1/oauth/authorize`
(`server/internal/app/server.go:662-663`) with PKCE, catches the redirect on the
loopback port, exchanges the code, then self-mints a 90-day PAT
(`cliPATExpiry`, `auth_browser.go:31`).

This only works when the browser runs on the same machine as the CLI. Over SSH,
in containers, on servers, or on any headless box the `localhost:PORT` redirect
never reaches the CLI. The current escape hatches are `--with-password` (classic
email/password, `auth.go:45`) and a manual headless token path (`auth.go:102`) —
both worse UX than what GitHub's `gh auth login` does.

The fix is the OAuth 2.0 Device Authorization Grant (RFC 8628), already
implemented in `../dbbat` (`internal/api/device.go`,
`internal/store/device_auth.go`): the CLI displays a short user code and a
verification URL, the user approves on **any** device/browser where they're
logged in, and the CLI polls the token endpoint until approval. One flow that
works on desktop *and* headless — it should become the default login mode.

## Proposal

Mirror dbbat's implementation, adapted to solidping's multi-tenant model and
handler/service conventions.

### Server (new `device` surface in `server/internal/handlers/auth/`)

- `POST /api/v1/auth/device` — unauthenticated device-authorization request.
  Accepts optional `clientName` (label shown on the consent page). Returns the
  RFC 8628 response: `device_code` (32 random bytes, the capability),
  `user_code` (8 chars from an ambiguity-free alphabet, displayed `XXXX-XXXX`),
  `verification_uri`, `verification_uri_complete`, `expires_in`, `interval`.
- `POST /api/v1/auth/device/token` — polling endpoint,
  `grant_type=urn:ietf:params:oauth:grant-type:device_code`. Answers with OAuth
  error codes (`authorization_pending`, `slow_down`, `expired_token`,
  `access_denied`) until approved; on approval returns a **named PAT** minted
  server-side at consent time (same shape the browser flow ends up with —
  reuse `cliPATTokenName()` naming, 90-day expiry).
- `GET`/`POST` consent endpoints (authenticated) — look up the request by
  normalized `user_code`, show client name + expiry, approve or deny. Approval
  binds the PAT to the approving user **and their org** (dbbat is
  single-tenant; solidping must record `organization_uid` — if the approving
  user belongs to several orgs, the consent page picks the org, defaulting to
  the current one).
- New table for pending device-auth requests, migrations in **both**
  `internal/db/postgres/migrations/` and `internal/db/sqlite/migrations/`.
  Single-use, short expiry (dbbat: 15 min), rows deleted/expired after use.
- Security posture from dbbat to keep: user-code lookups are the brute-forceable
  surface → rate-limit the consent lookup; device_code entropy is the real
  capability; poll interval enforced (`slow_down`). Reuse solidping's existing
  rate-limit middleware (`internal/middleware/ratelimit.go`).
- Add the endpoints to `server/internal/app/openapi/openapi.yaml` so the
  generated client covers them.

### Dashboard (dash0)

- Verification page at the `verification_uri` (e.g. `/dash0/device`), reachable
  from a logged-out state via the normal login redirect (`?returnTo=`), with the
  code pre-filled when `verification_uri_complete` is used. Follow the design
  reference; mobile-friendly (the whole point is approving from a phone).

### CLI

- `sp auth login` default becomes the device flow: print the user code +
  verification URL, best-effort `openBrowser()` to `verification_uri_complete`
  (GitHub-style), poll until approval or expiry, save the returned PAT via the
  existing `SaveTokens`/PAT persistence path.
- Keep `--with-password`. Drop the loopback-callback flow and its
  `oauth/authorize` client plumbing in `auth_browser.go` unless something else
  depends on it — one login flow, fewer moving parts. (The server-side
  `/oauth/authorize` endpoints stay: the dashboard OAuth providers use them.)

### Tests

- Backend: table-driven handler/service tests covering the full lifecycle —
  pending → approve → token delivered exactly once; deny; expiry; wrong
  user_code; poll-before-approve returns `authorization_pending`; `slow_down`
  on fast polling.
- E2E (Playwright): consent page renders, approve mints a PAT visible in the
  tokens list.

### Open questions

- Multi-org approval UX: org picker on consent vs. always the session's current
  org. Default to current org if no strong opinion.
- Whether `sp auth login --browser` should remain as an alias for the device
  flow (compat) or be removed outright.

## Resolved open questions

**Q: Multi-org approval UX: org picker on consent vs. always the session's
current org. Default to current org if no strong opinion.**

**Decision:** Render an org picker on the consent page, pre-selected to the
approving session's current org. When the approving user belongs to exactly one
org, render it as a read-only line rather than a selector. The approved PAT is
bound to the org that is selected at approval time — the request record must
store `organization_uid` from that selection, not from the session implicitly.

**Q: Whether `sp auth login --browser` should remain as an alias for the device
flow (compat) or be removed outright.**

**Decision:** Remove `--browser` outright, together with the loopback-callback
flow and its `oauth/authorize` client plumbing in `server/pkg/cli/auth_browser.go`.
Do not add a hidden or deprecated alias — an unknown-flag error is the intended
behavior. `--with-password` stays. (The server-side `/oauth/authorize` endpoints
stay too; the dashboard OAuth providers use them.)

## Implementation Plan

1. **Migrations** — `010_v0_9_0.{up,down}.sql` in both
   `server/internal/db/postgres/migrations/` and
   `server/internal/db/sqlite/migrations/`: a new `device_auth_requests` table
   (`uid`, `device_code`, `user_code`, `client_name`, `status`,
   `organization_uid`, `user_uid`, `token_uid`, `token_value`, `last_polled_at`,
   `expires_at`, `created_at`, `updated_at`) with unique indexes on
   `device_code` and `user_code`. New number, never a reuse of 009.

2. **Model + store** — `internal/db/models/device_auth.go`
   (`models.DeviceAuthRequest` + status constants + `NewDeviceAuthRequest`), new
   methods on the `db.Service` interface implemented identically in
   `internal/db/postgres/device_auth.go` and `internal/db/sqlite/device_auth.go`:
   create, get-by-user-code (live only), get-by-device-code (live only),
   compare-and-set resolve (pending → approved/denied, carrying user/org/PAT),
   record-poll (for `slow_down`), consume (hard delete, gates exactly-once
   delivery on `RowsAffected == 1`), and purge-expired. Cross-engine parity test
   in `internal/db/device_auth_test.go` under the shared harness.

3. **Service** — `internal/handlers/auth/device_service.go`: user-code
   generation on the ambiguity-free alphabet with collision retry, 32-byte
   device codes, normalization (`normalizeDeviceUserCode`) and display
   formatting (`XXXX-XXXX`), consent lookup, approve (mints a named PAT via the
   existing `CreatePAT` path with `cliPATTokenName()`-style naming and a 90-day
   expiry, bound to the org **selected at approval**), deny, and polling with
   `authorization_pending` / `slow_down` / `expired_token` / `access_denied`.

4. **Handlers + routes** — `internal/handlers/auth/device_handler.go`:
   `POST /api/v1/auth/device` (public), `POST /api/v1/auth/device/token`
   (public, RFC 8628 grant type, RFC 6749 §5.2 error shape),
   `GET /api/v1/auth/device/consent?userCode=…` and
   `POST /api/v1/auth/device/consent` (both authenticated). RFC field names stay
   snake_case on these endpoints, flagged in a code comment as an intentional
   deviation from the house camelCase style. The consent lookup gets a
   dedicated, stricter instance of the existing
   `middleware.RateLimiter` — the user code is the brute-forceable surface.

5. **OpenAPI** — add the four endpoints and their schemas to
   `internal/app/openapi/openapi.yaml`, then `go generate ./pkg/client/...` so
   the generated client stays in sync.

6. **Dashboard** — org-less `/device` route (mirrors `/mcp`) that resolves the
   org from the auth context and forwards to
   `/orgs/$org/account/device`, which lives under the `/orgs/$org` layout and so
   inherits the standard `?returnTo=` login redirect. The page pre-fills the code
   from `?user_code=`, renders client name + expiry, an org picker
   (pre-selected to the current org, read-only line when the user has exactly one
   org), and Approve / Deny. Built exclusively from design-reference primitives,
   mobile-first.

7. **CLI** — `server/pkg/cli/auth_device.go` implements the device flow and
   becomes the default of `sp auth login`; `--with-password` is added as the
   explicit password-login switch (alongside the existing `--email`/`--password`
   /`--token`), and `auth_browser.go` plus its loopback/PKCE/`oauth.authorize`
   client plumbing and tests are removed outright (`openBrowser` and
   `cliPATTokenName` survive in the new file).

8. **Tests** — table-driven service/handler tests for the full lifecycle
   (pending → approve → delivered exactly once, second poll after delivery does
   not re-deliver; deny; expiry; wrong user code; poll-before-approve;
   `slow_down`; org binding incl. the multi-org case), CLI unit tests for code
   formatting/normalization and the polling loop, and a Playwright E2E covering
   the consent page rendering and an approval producing a PAT in the tokens list.
