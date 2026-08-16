# Live updates silently unavailable — WS connects but registers nothing; add E2E coverage of event delivery

## Problem

On https://solidping.k8xp.com/dash0/orgs/acmetech/checks the dashboard shows the
"Live updates unavailable" badge and no realtime updates arrive, with no
explanation. The WebSocket itself connects fine — the upgrade returns `101
Switching Protocols` — but the connection never gets past auth: the client sends
its `auth` frame and nothing else ever happens (no `hello`, no `subscribe`, no
events).

Captured HAR (`bad_ws.priv.har`, 2026-07-09 23:29 UTC) of the failing session:

- `GET wss://solidping.k8xp.com/api/v1/orgs/acmetech/events/ws` → `101` OK.
- The client sends exactly one frame: `{"type":"auth","token":"eyJ…"}`.
- **The JWT in that frame decodes to `"orgSlug":"test"`** — not `acmetech` —
  with `role":"user"`, `iat` 2026-07-09T23:28:51Z (issued ~17 s before the dial,
  so it is a *fresh* token for the wrong org, not an expired one).
- No server frames and no further client frames appear; the UI lands on the
  "Live updates unavailable" badge.

Server-side this is exactly the deny path in
`server/internal/handlers/realtimews/handshake.go:162-168`
(`authenticate`): for a non-super-admin, `claims.OrgSlug != orgSlug` →
`CloseForbidden` ("access to this organization is denied"), socket closed, no
`hello`. So "connects but doesn't register to anything" is the client dialing,
failing org validation, and (apparently) never recovering — while the rest of
the page keeps rendering from cached REST data, making the failure look
inexplicable.

## Likely root cause (to confirm)

The dash0 client keeps **one global token per browser profile**, not per org:

- `web/dash0/src/api/client.ts:31` / `:72` — a single `TOKEN_KEY` (+
  `solidping_org` at `:119`) in `localStorage`, shared by every tab on the
  domain.
- `web/dash0/src/lib/live-socket.ts:200` — the WS run-loop reads that global
  `getToken()` on every dial and sends it as the `auth` frame (`:296`).

So a login or token refresh performed for another org (e.g. a second tab open
on the `test` org of the same deployment, or an org switch) clobbers the token
the `acmetech` tab relies on. The next WS dial picks up the wrong-org token →
4403 → badge. The freshness of the wrong-org token in the HAR (`iat` 17 s
before the dial) fits a concurrent refresh/login under org `test`.

Related inconsistency noticed while tracing (worth aligning while in there):
the WS handshake lets a **DB** super-admin (`user.SuperAdmin`) cross orgs
(`handshake.go:162`), while the REST middleware only exempts **claims**
super-admins and 403s a DB super-admin with a mismatched `claims.OrgSlug`
(`server/internal/middleware/auth.go:172-178`). The two paths claim to mirror
each other; today they don't.

## Proposal

Primary deliverable requested: **an end-to-end test around live updates**, so
this class of failure can't regress silently again.

1. **E2E happy path — event delivery, not just subscription.** The existing
   `web/dash0/e2e/check-live-subscription-slug.spec.ts` already covers a
   subscribe rejection surfacing the badge, but nothing verifies events
   actually flow. Add a Playwright spec (`web/dash0/e2e/`, patterned on that
   file) that:
   - logs in, opens the checks page, and asserts the full handshake on the real
     socket: `auth` sent → `hello` received → `subscribe` sent → `subscribed`
     ack (assert via Playwright's WebSocket frame inspection, as the existing
     spec does);
   - then triggers a server-side change that emits a live event (e.g. create /
     rename / toggle a check via the REST API) and asserts the UI updates
     **without a reload** — this is the "does it register to anything" gap the
     incident exposed.

2. **E2E regression — wrong-org token on the auth frame.** Simulate the
   observed failure: seed `localStorage` with a valid token for a *different*
   org (obtainable via the login API for a second org, or by overwriting
   `TOKEN_KEY` mid-session), navigate to the first org's checks page, and
   assert:
   - the socket is closed by the server (4403) and no `hello` arrives;
   - the "Live updates unavailable" badge appears (today's behavior at
     minimum — see fix below for the desired end state).

3. **Fix direction (scope to confirm before implementing).** The test in (2)
   documents current behavior; the actual fix likely involves one or both of:
   - client: on a 4403 close, re-resolve credentials for the org in the URL
     (or at least surface *why* live updates are unavailable) instead of
     parking on the badge forever;
   - client: make token storage org-aware so one tab's login/refresh can't
     poison another org's tab (this affects REST too, not just WS).
   - server: align the WS handshake's and REST middleware's super-admin/org
     checks (see inconsistency above).

## Open questions

- Why did the browser hold a fresh `test`-org token while browsing
  `acmetech`? Second tab, org switcher, or an auto-refresh racing a
  navigation? Worth reproducing before choosing the client fix.
- Should REST requests on the page have failed too (same clobbered token)?
  If they visibly kept working from TanStack Query cache, the badge may be the
  only signal we get — which strengthens the case for a more explicit
  "reconnect / re-login" affordance on 4403.

## Implementation Plan

### Findings that shape the plan
- **Event-delivery-without-reload is already partly covered.** `web/dash0/e2e/live-updates.spec.ts`
  drives a heartbeat check through the public heartbeat endpoint and asserts the
  dashboard/detail page updates live. But nothing asserts the *full frame handshake*
  (`auth` sent → `hello` received → `subscribe` sent → `subscribed` ack) explicitly — the
  existing helper only waits for a `subscribed` ack. That explicit assertion is the gap in (1).
- **Check CRUD does not emit realtime hints.** Create/rename/toggle a check publishes no
  `checks`/`results` hint (verified: `server/internal/handlers/checks/service.go` only calls the
  domain-event `eventNotifier.Notify`, not the realtime publisher). The reliable, deterministic
  server-side trigger for a live UI change is a **heartbeat push** (`s.rt.Publish(..., KindResults)`
  in `server/internal/handlers/heartbeat/service.go`), exactly as the existing spec uses.
- **The test user is a super-admin.** `test@test.com` is seeded with `SuperAdmin=true` and login
  mints `Role: "superadmin"` claims, so `claims.IsSuperAdmin()` is true and the WS handshake org
  check is bypassed. To reproduce a real server 4403 we need a *non-super-admin* token whose
  `orgSlug` claim differs from the URL org. Mint one by seeding a fresh user via the test-only
  `POST /api/v1/test/users` endpoint, then `POST /api/v1/orgs` to create an org for them — the
  response carries a fresh org-scoped, role=`admin` (non-super-admin) token.
- **The "Live updates unavailable" badge is the sidebar `live-status-dot`.** A 4403 close →
  `onDisabled()` → status `"disabled"` (`data-status="disabled"`, tooltip `liveStatus.unavailable`).
  A generic bad-token/network close → `"reconnecting"` instead, so asserting `"disabled"`
  specifically distinguishes the server-side forbidden path.
- **Server inconsistency confirmed.** `realtimews/handshake.go` `authenticate()` skips the whole
  org check for a **DB** super-admin (`user.SuperAdmin`), while REST `middleware/auth.go`
  `RequireOrgAccess` only exempts **claims** super-admins for the org-slug-match step and 403s a
  DB-only super-admin with a mismatched `claims.OrgSlug`. In practice a DB super-admin always gets
  super-admin claims, so the divergence is currently unreachable — but it is a real, cheap,
  testable alignment.

### Steps
1. **Server-side alignment (secondary, low-risk).** Restructure `realtimews/handshake.go`
   `authenticate()` to mirror `RequireOrgAccess` exactly: (a) only a *claims* super-admin may cross
   orgs — `if !claims.IsSuperAdmin() && claims.OrgSlug != orgSlug → CloseForbidden`; (b) the
   membership check stays gated on `!claims.IsSuperAdmin() && !user.SuperAdmin`. Add a Go test in
   `server/internal/handlers/realtimews/handler_test.go` locking it in: a DB super-admin
   (`user.SuperAdmin=true`) with a token minted via `GenerateTokensForOAuth(user, otherOrg, "admin")`
   (claims `orgSlug="other"`, role `admin`) dialing the `test` org WS must now close `CloseForbidden`.
   Seed org `test` first so the check reached is the org-mismatch, not "organization not found".
2. **E2E happy path — full handshake + event delivery** (new file
   `web/dash0/e2e/live-updates-handshake.spec.ts`). Create a heartbeat check via REST, register
   frame listeners on the `/events/ws` socket before navigating to the check detail page, then
   assert the full ordered handshake (`auth` sent → `hello` received → `subscribe` sent →
   `subscribed` ack) via Playwright `framesent`/`framereceived` inspection. Then send an `up`
   heartbeat and assert the UI flips to "Currently up for …" without a reload.
3. **E2E regression — wrong-org token on the auth frame** (same new file). Seed a fresh
   non-super-admin user + their own org (token `T_B`, `orgSlug=B`), seed `T_B` into `localStorage`
   before load, navigate to the `test` org checks page, and assert: an `auth` frame is sent, no
   `hello` is ever received, the socket closes, and the sidebar `live-status-dot` reaches
   `data-status="disabled"` (never `"live"`) — documenting today's behavior (badge appears). Skip
   cleanly if the test-only user-seed endpoint is unavailable (server not in `SP_RUNMODE=test`),
   mirroring `create-org.spec.ts`.
4. **QA.** `make build-backend lint-back test` for the Go change; `make build-dash0` +
   `cd web/dash0 && bun run lint` for the E2E files. Author the E2E and run it if a test server is
   reachable; otherwise report authored-but-not-run.

### Explicitly deferred (scope-to-confirm, high risk)
- The org-aware **client token storage** rewrite of `web/dash0/src/api/client.ts` (one global token
  per profile → per-org) and client **recovery on 4403** (re-resolve credentials / surface why live
  is unavailable) are left for the user. The regression E2E documents the current badge behavior,
  which the spec accepts as satisfying "document current behavior".
