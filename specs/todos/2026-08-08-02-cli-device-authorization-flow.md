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
