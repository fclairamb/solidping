# Session dies ~1h after login and the live socket churns forever on an expired token — fix the token-refresh chain, make max session time per-org configurable

## Problem

Users get "disconnected" from the dashboard far too quickly — in practice
about one hour after login — and end up back on the login page. The frontend
is supposed to keep refreshing its access token indefinitely (bounded only by
the 7-day sliding refresh window), so a session should survive as long as the
tab is in use.

There is also no way to control session length per organization: the TTLs are
global deployment config (`server/internal/config/config.go:668-669`,
`AccessTokenExpiry: time.Hour`, `RefreshTokenExpiry: 7*24h`), and the desired
"maximum session time" should be a per-org setting editable in the UI.

### Evidence: captured disconnect (2026-07-08, solidping.k8xp.com)

`401.priv.har` (repo root, gitignored via `*.priv*`) is a DevTools export
filtered to the live WebSocket `/api/v1/orgs/webingenia/events/ws`. What it
shows:

- The JWT in every `auth` frame has `iat` 12:47:59Z / `exp` 13:47:59Z — the
  1-hour `AccessTokenExpiry` default.
- The capture is 139 connections over 70 minutes (13:48:02Z → 14:58:31Z),
  starting **3 seconds after token expiry**. Every connection: open → client
  sends one `auth` frame → closed ~40-150 ms later. **Zero server frames** —
  no `hello`, nothing.
- Every one of the 139 `auth` frames carries the **same expired JWT** (same
  `iat`, first/middle/last verified) — localStorage's access token was never
  rotated for 70+ minutes while JS was demonstrably running.
- The first reconnect gaps are ~4 s / 7 s / 15 s / 37 s, then steady
  ~22-40 s: exponential backoff from a **reset** attempt counter hitting its
  cap. The counter only resets on a successful `hello`
  (`web/dash0/src/lib/live-socket.ts`), so the pre-expiry socket *had*
  authenticated and was closed at expiry — the server closes an
  authenticated socket with 4401 when its token expires
  (`server/internal/handlers/realtimews/handler_test.go:639`, code
  `CloseAuthFailed = 4401` at `handler.go:35`).

So: a healthy session hit access-token expiry, and **all three refresh
layers failed silently**, leaving a zombie tab that hammered the server with
a dead token every ~30 s (139 useless upgrades/goroutines from one tab).

### The three refresh layers that should have prevented this

1. **Proactive timer** — `AuthContext.tsx:191-199`: every 60 s, refresh once
   less than 1/3 of the token lifetime remains (`shouldRefreshNow`,
   `web/dash0/src/lib/token-refresh.ts:78-88`). Should have fired from
   ~13:28. Didn't (token kept its 12:47 `iat`).
2. **Refresh-on-4401** — `live-socket.ts:294-302`: every 4401 close calls
   `refreshAccessToken()` before the next attempt re-reads the token from
   storage (`live-socket.ts:200`). 139 opportunities, none produced a new
   token.
3. **Reactive 401** — `api/client.ts:180-192`: any API call 401s → refresh →
   retry. Only fires if the page makes API calls; TanStack Query polling is
   paused in background tabs by default, so an idle/backgrounded tab never
   exercises it. When the user finally interacts, the retry fails and
   `handleResponse` clears the session and redirects to login
   (`client.ts:197-206`) — the observed "disconnect".

### Diagnosis

The HAR pins down two facts: the token was never refreshed, **and** the
session was never cleared (attempts kept coming with a token — if refresh
had received a non-OK response, `doRefresh` would have called `clearToken()`
(`token-refresh.ts:35-38`) and the socket loop stops dialing when
`getToken()` is null (`live-socket.ts:200-204`)).

Per `token-refresh.ts:22-51`, `doRefresh` resolves `null` *without side
effects* in exactly two cases:

- **`getRefreshToken()` returns null** (`token-refresh.ts:23-26`) — no
  refresh token in localStorage; or
- the `fetch` throws (network error, `token-refresh.ts:46-50`) — implausible
  ×139 while WSS upgrades to the same origin succeed in ~50 ms.

**Leading hypothesis: the session in localStorage had an access token but no
refresh token (and/or no `solidping_expires_at`/`_in`).** That single state
silently disables all three layers: layer 1 because
`shouldRefreshNow(null, null)` returns `false` by design ("a session
predating this field", `token-refresh.ts:74-76`); layers 2 and 3 because
`doRefresh` no-ops. `setSession`'s own docstring names the failure class:
every login-shaped response (password, 2FA, passkey, OAuth, Slack,
switch-org, refresh) must funnel through it "or the refresh token and expiry
tracking silently go missing for that path" (`api/client.ts:60-67`).
Candidate origins: a login path that bypasses `setSession`, or a legacy
session predating the refresh-token feature.

To rule out during implementation: a proxy/ingress stripping the 4401 close
code (would disable layer 2 only — doesn't explain layer 1's silence), and
refresh POSTs actually failing (the HAR is WS-filtered, so they're not
visible).

### Design defects, regardless of the exact trigger

- Refresh failure is **indistinguishable from success** for layers 1 and 2:
  `null` return, no log, no user-facing state change, no escalation.
- The socket loop **never gives up or escalates**: it reconnects forever
  with a token it could know is expired (the JWT `exp` is readable
  client-side). Its comment assumes "the reactive apiFetch 401 path will
  already be steering the user to login by then" — disproven by 70 minutes
  of churn.
- A session with a token but no expiry metadata is treated as "nothing to
  schedule" instead of "refresh immediately / re-login".
- Session length is global-only; no per-org control, and the 7-day refresh
  window is a sliding *idle* timeout with no absolute cap.

## Proposal

### A. Make the frontend keep a session alive — and fail loudly when it can't

In `web/dash0`:

1. **Funnel audit**: verify every login-shaped path stores the full session
   via `setSession(access, refresh, expiresIn)` — password, 2FA, passkey,
   OAuth (GitHub/Google), Slack, switch-org. Fix any that set only the
   access token; add a test that fails when a new auth path skips it.
2. **Differentiate refresh outcomes** in `token-refresh.ts`: "no refresh
   token", "server rejected (non-OK)", "network error". The first two while
   the app believes it is authenticated → clear the session and redirect to
   expired-login *immediately* (today only the reactive path does, and only
   on user interaction). Network errors keep the current
   retry-quietly behavior.
3. **Stop the zombie socket loop**: before dialing, skip/refresh a token
   whose `exp` is already past; after a 4401 close where
   `refreshAccessToken()` resolves null for a non-network reason, stop
   reconnecting and surface the expired session (redirect), rather than
   retrying the same dead token at the backoff cap forever.
4. **Legacy/partial sessions**: token present but `expires_at`/`expires_in`
   missing → attempt one refresh on load; if it can't produce a full
   session, treat as expired instead of coasting until a surprise 401.
5. **Observability**: log (console + a counter if cheap) on every refresh
   failure with its differentiated reason, so the next HAR isn't needed.

### B. Session TTL: a global parameter with a per-org override

The session TTL is a **global parameter** that each org may **override** —
both levels live in the existing `parameters` table
(`server/internal/db/models/parameter.go:11`), whose nullable
`organization_uid` already distinguishes system rows (NULL) from org rows.

1. **Global level** — register `auth.session_max_duration` (seconds) as a
   known system parameter (`getKnownParameters`,
   `server/internal/systemconfig/systemconfig.go:104`), runtime-editable
   through the existing super-admin `/api/v1/system/parameters` API
   (`server/internal/app/server.go:893-916`) and applied onto the config at
   startup like the other keys (`server.go:2090`). Default when unset:
   **unlimited** (today's behavior — no absolute cap, only the sliding
   window).
2. **Per-org override** — an org-scoped row with the same key, read via
   `GetOrgParameter` (`server/internal/db/service.go:353`). Resolution
   order: **org parameter → system parameter → config default**.
3. **Semantics** — a **hard absolute cap** on session lifetime, measured
   from login: the refresh row's `expires_at` becomes
   `min(now + RefreshTokenExpiry, login_time + session_max_duration)` at
   mint and in `slideSessionExpiry` — the 7-day sliding *idle* window keeps
   working inside the cap. Centralize this in one helper the auth service
   calls with the org in hand: `cfg.RefreshTokenExpiry` is currently read
   at four mint/slide sites (`handlers/auth/service.go:585`, `:1017`,
   `:1637`, `:1835`) and the helper replaces all of them. A per-login DB
   read is fine (mints are rare); cache like `patCacheDuration` if it ever
   shows up.
   Access-token TTL stays global-only for now (see Open questions).
4. **UI** — the org override is edited on the org settings page
   (`web/dash0/src/routes/orgs/$org/organization.settings.tsx`), following
   the design-reference primitives. API surface: extend the org PATCH the
   settings page already uses (preferred), or a narrow
   `PUT /api/v1/orgs/:org/parameters/:key` gated to an allowlist of
   org-overridable keys. The global value is edited via the existing
   system-parameters API (super-admin).

*Rejected alternative — entitlements payload*: the billing service PUTs the
whole `org_entitlements` payload on sync, so an operator-set session policy
stored there would be silently clobbered; entitlements stay billing-driven
limits, session TTL is an operational parameter.

## Out of scope

- Refresh-token rotation (deliberately not rotated today,
  `token-refresh.ts:41-44`).
- Changing the 10-active-sessions cap (`service.go:72`).
- Server-side WS protocol changes (an error frame before the 4401 close
  would be nice for debuggability but isn't needed for the fix).
- The deprecated `web/dash` app.

## Acceptance criteria

- **E2E (Playwright, short `access_token_expiry` in test config)**: a
  logged-in dashboard left open across an access-token expiry keeps working
  — API calls succeed and the live socket re-authenticates with a fresh
  token after the 4401 — with no re-login.
- **E2E/unit**: a localStorage session with an access token but no refresh
  token redirects to login promptly (no unbounded reconnect churn; the
  socket makes at most ~2 attempts with a dead token).
- Unit: refresh outcome differentiation; expired-`exp` pre-dial check;
  `shouldRefreshNow` legacy-null behavior replaced per A.4.
- Session TTL resolution: org parameter beats system parameter beats
  default (unit-tested through the new helper); refresh rows for an org
  with an override never extend past `login_time + session_max_duration`;
  orgs without one follow the system parameter; neither set = today's
  behavior (no cap).
- The org override round-trips through the org settings UI, and the global
  value through `/api/v1/system/parameters`.
- `make test` and `make lint` green.

## Open questions

- What created the refresh-token-less session? The A.1 audit should answer
  it — if a live path is found, note it in the fix; if not, chalk it up to
  a legacy session and let A.4 absorb the class.
- Should the same parameter (or a sibling `auth.access_token_ttl`) also
  drive the **access**-token TTL (longer access TTL = fewer refreshes and
  fewer hourly WS 4401 blips, at the cost of slower revocation)? Default
  answer: no, session cap only — but the resolution helper makes adding it
  cheap later.
- Who may edit the org override — org admins on their own org (default),
  or super-admin only? The global parameter is super-admin by construction.
- At the hard cap, force logout even mid-activity, or warn first? Default:
  redirect to login with `?returnTo=` (per `wiki/conventions/frontend-errors.md`).

## Implementation plan

- [ ] A.1 funnel audit + test; A.2 differentiated refresh outcomes +
      immediate escalation; A.3 socket pre-dial `exp` check + give-up on
      failed 4401 refresh; A.4 legacy-session handling; A.5 logging.
- [ ] B.1-B.3 `auth.session_max_duration` system parameter + org override,
      org→system→default resolution helper replacing the four
      `RefreshTokenExpiry` read sites, hard-cap semantics + backend tests.
- [ ] B.4 org settings UI (+ API surface for the org override) + E2E.
- [ ] Verify: `make test`, `make lint`, the two E2E scenarios above; manual
      check on solidping.k8xp.com that an expired-token tab recovers.

## Implementation Plan

### A. Frontend token-refresh reliability

1. **A.1 funnel audit** (`web/dash0`): grep every `setSession`/`setToken`
   call site. Found three real gaps:
   - `main.tsx`'s pre-React OAuth handoff IIFE called `setToken(accessToken)`
     only, then immediately stripped the URL via `history.replaceState` —
     dropping `refresh_token`/`expires_in` that the backend redirect already
     carries (`server/internal/handlers/auth/{github,google,slack}.go`
     `buildSuccessRedirect`) *and* making `$org.tsx`'s equivalent
     `loginWithOAuth` effect permanently unreachable (its query-param read
     always sees the already-stripped URL). This is the leading-hypothesis
     bug from the spec's Evidence section: exactly explains "access token,
     no refresh token" sessions for GitHub/Google/Slack logins. Fix: extract
     the handoff into a testable `lib/oauth-handoff.ts` (`parseOAuthHandoff`
     + `applyOAuthHandoff`, calling `setSession` with all three fields);
     `main.tsx` becomes a thin caller. Add unit tests.
   - `confirm-registration.$token.tsx` called `setToken(data.accessToken)`
     only, even though the backend's `ConfirmRegistration` mints a full
     `LoginResponse` (refresh token + expiry included) exactly like a
     password login. Fix: widen `useConfirmRegistration`'s response type and
     call `setSession(accessToken, refreshToken, expiresIn)`.
   - `no-org.tsx`'s `useCreateOrg` handler called
     `if (result.accessToken) setToken(result.accessToken)`, but the
     backend's `CreateOrg`/`OrgResponse` never returns an `accessToken`
     field — dead code hiding behind a mistyped (falsely non-optional)
     response type. Fix: drop the dead line and the phantom field from the
     hook's type; this is not a live bug (no token to bypass with), just a
     misleading no-op, and is called out as a distinct, out-of-scope,
     pre-existing org-scoping gap (the existing token has no claim to the
     brand-new org and org-scoped endpoints will 403 per
     `middleware/auth.go`'s `claims.OrgSlug != orgSlug` check) — flagged
     separately, not fixed here.
   - All other login-shaped paths (password/2FA/recovery via
     `applyLoginResponse`, passkey via `applyLoginResponse`, `switchOrg`,
     `acceptInviteSession`) already call `setSession` correctly.
   - Delete the `setToken` export from `api/client.ts` entirely once its
     three callers are fixed — turns "a new auth path bypasses setSession"
     into a compile error instead of a silent gap.
2. **A.2 differentiated refresh outcomes** (`lib/token-refresh.ts`): change
   the internal `doRefresh` to return `{ accessToken, failureReason? }`
   where `failureReason` is `"no-refresh-token" | "rejected" |
   "network-error"`. Add `escalate()`: for the first two reasons, clear the
   session and call (newly exported) `redirectToExpiredLogin` from
   `api/client.ts` immediately, inside the single-flight refresh itself (so
   it fires once regardless of how many concurrent callers are waiting) —
   not waiting for the next 401/interaction. Network errors keep retrying
   quietly (unchanged). `refreshAccessToken()` keeps its `Promise<string |
   null>` signature for existing callers; new `refreshWithOutcome()` exposes
   the reason for callers (live-socket, AuthContext) that need to decide
   whether to keep retrying.
3. **A.3 socket pre-dial check + give-up** (`lib/live-socket.ts`): before
   dialing, read `getExpiresAt()`; if already past, call
   `refreshWithOutcome()` first instead of connecting with a token already
   known dead. On the 4401-close handler, use `refreshWithOutcome()` instead
   of `refreshAccessToken()`: reconnect with backoff only on success or a
   network-error outcome; on `"no-refresh-token"`/`"rejected"` (escalation
   already fired inside token-refresh.ts), resolve the connect attempt as
   `"disabled"` so the `run()` loop stops reconnecting instead of hammering
   the server with a dead token at the backoff cap forever.
4. **A.4 legacy/partial sessions** (`contexts/AuthContext.tsx`): in
   `validateSession` (runs once on mount), if there's a token but
   `getExpiresAt() === null` (no expiry metadata — legacy session, or one of
   the funnel-audit gaps before this fix), call `refreshWithOutcome()` up
   front. A non-network failure means escalate() already redirected —
   short-circuit instead of also trying `/me`. Success or a network blip
   falls through to the existing `/me` validation.
5. **A.5 logging**: `logRefreshFailure()` in token-refresh.ts logs every
   refresh failure with its reason — `console.error` for the two escalating
   reasons (lands in the existing bug-report ring buffer,
   `components/feedback/errorCollector.ts`, for free) and `console.warn` for
   network errors.

### B. Session TTL: system parameter + per-org override

6. **B.1 system parameter**: register `auth.session_max_duration` (seconds,
   int, default unset = unlimited) in `getKnownParameters`
   (`server/internal/systemconfig/systemconfig.go`); apply onto
   `cfg.RefreshTokenExpiry`-adjacent config at startup like existing keys.
7. **B.2 resolution helper**: add a helper on the auth service (org UID →
   `time.Duration`, `0` meaning "no cap") that resolves org parameter →
   system parameter → config default via `GetOrgParameter` /
   `GetSystemParameter`, and replaces the four direct
   `cfg.RefreshTokenExpiry` reads in `handlers/auth/service.go` (mint sites
   at password/OAuth/passkey login, registration, invite-accept, switch-org,
   and the refresh/slide path). Cap semantics: refresh row `expires_at =
   min(now + RefreshTokenExpiry, login_time + session_max_duration)` at mint
   time; the slide-on-refresh path re-applies the same cap so an org
   override can't be exceeded by sliding.
8. **B.3 tests**: unit tests for the resolution helper (org beats system
   beats default; neither set = unlimited); a service-level test that an
   org with a short override never mints/slides a refresh row past
   `login_time + session_max_duration`.
9. **B.4 UI + API**: expose `sessionMaxDurationSeconds` (nullable = inherit)
   on the org PATCH the settings page already uses; render the override
   field on `organization.settings.tsx` using design-reference primitives
   (labeled input + "inherit default" affordance). Global value stays
   editable only via the existing `/api/v1/system/parameters` (super-admin).

### Verification

10. Backend: `make build-backend lint-back test`.
11. Frontend: `make build-dash0`, `cd web/dash0 && bun run lint` (no new
    errors in touched files — dash0 `eslint` is red on ~45 pre-existing
    `react-hooks` errors on base).
12. E2E: author (and run if the local devloop supports test-mode auth) a
    Playwright scenario for access-token-expiry recovery and the
    legacy-session (no refresh token) prompt redirect.
