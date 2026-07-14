---
model: opus
effort: high
---

# Move WebSocket authentication to the HTTP level and drop the in-band `auth` message

## Problem

The realtime WebSocket connection currently supports authenticating with an
in-band first message:

```
{"type":"auth","token":"..."}
```

But sometimes that token is stale (e.g. carried over from a previous page /
previous connection), so the connection authenticates with a bad token. The
in-band auth path adds complexity and a class of "authenticated with the wrong
token" bugs.

Source issue: [#128 — dash0/server: Bad handling of WS](https://github.com/fclairamb/solidping/issues/128).
The issue asks to:
- Use the token passed via headers like any other request,
- Remove the whole authentication logic on the WS itself (it should live at the
  HTTP level only),
- Respond at the HTTP level if there is an error.

Current handshake logic is in
[`server/internal/handlers/realtimews/handshake.go`](server/internal/handlers/realtimews/handshake.go):
it first tries the `Authorization` header, then the `access_token` cookie, and
finally falls back to `awaitAuthMessage` (the in-band `{"type":"auth"}` grace
window). Related: `messages.go`, `handler.go`, `handler_test.go` in the same
package. The browser client sends the in-band auth message today, so the frontend
must switch to passing the token at connect time.

## The browser constraint that shapes the protocol

A browser **cannot read the HTTP status of a *failed* WebSocket handshake**. If
the server answers the upgrade `GET` with anything other than `101`, the browser
`WebSocket` fires `onclose`/`onerror` with code **`1006` and an empty reason** —
the real status (`401`/`403`/`404`) and body are never exposed to JS. This is
precisely why the current code upgrades first and then closes with app-defined
codes (`4401`/`4403`/`4404`, in
[`handler.go`](server/internal/handlers/realtimews/handler.go)): those codes
*are* readable by the browser, an HTTP status is not.

So "respond at the HTTP level" is fully achievable for **explicit-auth clients**
(CLI/curl/websocat/tests — they read HTTP responses), and is the right answer for
them. For the **browser** it only helps for the token-validity check (the client
treats any pre-`hello` failure as a retryable drop and self-heals), while the two
states the browser must act on *differently* — permanent stop — must stay
readable close codes. The protocol below splits along exactly that line.

## Proposal

Move the **token-authentication** step ahead of `websocket.Accept` in `Serve`;
authenticate from the `Authorization: Bearer` header **or** the `access_token`
cookie (header wins), exactly like `middleware.extractToken`. Both transports are
supported and coexist. Keep org-scope and feature-disabled where they are today
(post-upgrade close codes) — see the table.

### Handshake protocol

| Outcome | Header client (explicit auth) | Cookie client (browser) |
|---|---|---|
| Valid token + org access | `101` upgrade, then `hello` | `101` upgrade, then `hello` |
| Missing / invalid / expired token, or user not found | **HTTP `401`** + standard JSON error, no upgrade | **HTTP `401`**, no upgrade (browser sees `1006`) |
| Valid token, not an org member | `websocket.Accept` → close **`4403`** | `websocket.Accept` → close **`4403`** |
| Realtime disabled (`SP_REALTIME_ENABLED=false`) | `websocket.Accept` → close **`4404`** | `websocket.Accept` → close **`4404`** |
| Token expires mid-connection | close **`4401`** (unchanged) | close **`4401`** (unchanged) |

Rationale for the split: **token validity → HTTP `401` before upgrade** delivers
the "explicit auth gets an answer" guarantee to header clients and, for the
browser, removes the stale-in-band-token bug entirely (the browser now
authenticates with the auto-attached, always-fresh cookie). **Org-scope (`4403`)
and feature-disabled (`4404`) stay post-upgrade close codes** because those are
the only outcomes the browser must handle differently (permanent stop →
`onDisabled` → fall back to polling), and it can only read them as WS close
codes. Keeping them post-upgrade is also the minimal change: the dash0 client's
`4403`/`4404`/`4401` handling in
[`live-socket.ts`](web/dash0/src/lib/live-socket.ts) is untouched.

### Server changes

1. In `Serve`, before `websocket.Accept`: extract the header-or-cookie token and
   validate it (signature/expiry + user exists). On failure write **HTTP `401`**
   with the standard error shape and return — no upgrade, no dangling socket.
2. Keep the feature-disabled check and the org-scope check
   (`authenticate`'s `4403` branch) after the upgrade, closing with `4404`/`4403`
   as today.
3. **Delete `awaitAuthMessage` and the whole in-band `{"type":"auth"}` path**
   (`handshake.go`), the auth grace-window (`authGrace`), and the `auth`
   branch/`msgTypeAuth` handling in `messages.go`. The cookie-fallback
   comment in `handshake.go` about "give the fresh in-band auth message a chance"
   goes away — an invalid cookie now simply fails at the HTTP layer.
4. **Do not put the token in a query string.** Header or cookie only.

### Frontend changes (dash0)

5. Remove the `sock.onopen` in-band send `sock.send({type:"auth",token})`
   ([live-socket.ts:296](web/dash0/src/lib/live-socket.ts:296)); the browser
   attaches the `access_token` cookie to the same-origin handshake automatically.
   Drop the now-unused `token` threading into `connectOnce` **but keep the
   run()-loop pre-dial refresh** ([live-socket.ts:207-228](web/dash0/src/lib/live-socket.ts:207))
   — it is now what guarantees the cookie in play is live (see Q1).
6. `onclose` handling for `4401`/`4403`/`4404` stays as-is; a pre-`hello` `1006`
   is already treated as a retryable drop, so a browser whose cookie is somehow
   invalid refreshes (re-setting a live cookie) and reconnects — self-healing.

### Tests

7. `handler_test.go`: (a) valid header token → `101` + `hello`; (b) valid cookie
   token → `101` + `hello`; (c) missing/invalid token → **HTTP `401`, no
   upgrade** (assert status, not a close code); (d) header-over-cookie
   precedence; (e) valid token wrong org → close `4403`; (f) the in-band `auth`
   path is gone (a first-message `auth` frame is treated as any other frame, not
   as authentication). Add/adjust a Playwright E2E so the browser cookie +
   reconnect path is exercised end-to-end.

## Open questions — answered

**Q1. Does the `access_token` cookie reliably carry the *current* token at
connect time?** — **Yes, given the existing refresh path; no new re-mint
mechanism is needed.** Both `/api/v1/auth/login`
([handler.go:116](server/internal/handlers/auth/handler.go:116)) and
`/api/v1/auth/refresh`
([handler.go:204](server/internal/handlers/auth/handler.go:204)) re-set the
cookie with `Value: resp.AccessToken, MaxAge: resp.ExpiresIn`, and the dash0
refresh path POSTs `/api/v1/auth/refresh`
([token-refresh.ts:75](web/dash0/src/lib/token-refresh.ts:75)) so the browser
re-stores the fresh cookie on every refresh. The run()-loop already refreshes a
known-dead token *before* dialing the socket
([live-socket.ts:207-228](web/dash0/src/lib/live-socket.ts:207)), which re-mints
the cookie before the handshake — so a suspended tab reconnects with a live
cookie. The old handshake comment's "may be a stale OAuth-flow cookie" caution
predates the refresh-endpoint cookie re-sync and is obsolete once the in-band
fallback is removed. **Action:** keep the pre-dial refresh; no extra work.
- Minor note: the cookie is currently set without `Secure`/`HttpOnly`/`SameSite`
  ([handler.go:116-121](server/internal/handlers/auth/handler.go:116)). Since the
  token also lives in `localStorage`, `HttpOnly` would not be a new exposure; the
  same-origin WS handshake relies on the default `SameSite=Lax` still attaching
  the cookie (it does, for same-origin). Out of scope to change here, but noted.

**Q2. What do non-browser clients use today?** — **The `Authorization: Bearer`
header**, confirmed by `extractHeaderToken` in `handshake.go`, the curl examples
in `server/CLAUDE.md`, and `handler_test.go`. The header path is preserved and is
exactly the "explicit auth gets an HTTP answer" path above.

## Implementation Plan

### Backend (`server/internal/handlers/realtimews/`)
1. **`handshake.go` — pre-upgrade token auth + post-upgrade org authz.**
   Replace the `handshake`/`awaitAuthMessage` machinery with:
   - `extractToken(req)` mirroring `middleware.extractToken` (Authorization
     header wins; `access_token` cookie fallback only when no header present),
     plus `extractHeaderToken`/`extractCookieToken` helpers.
   - `authenticateToken(ctx, token) → (claims, user, base.ErrorCode, msg)`:
     empty token → `NO_TOKEN`; `ValidateToken` fail → `INVALID_TOKEN`; user
     missing → `USER_NOT_FOUND`. Runs **before** `websocket.Accept`.
   - `authorizeOrg(ctx, claims, user, orgSlug) → (org, closeCode, reason)`:
     org lookup + super-admin/org-slug/membership checks → `4403` on failure.
     Runs **after** the upgrade (browser must read it as a close code).
   Delete `awaitAuthMessage`, the grace window, and the stale cookie comment.
2. **`handler.go` — reorder `Serve`.** Extract+authenticate the token first; on
   failure write **HTTP 401** (`base.HandlerBase.WriteError`, standard shape) and
   return without upgrading. Then `websocket.Accept`; then feature-disabled
   (`4404`); then `authorizeOrg` (`4403`); then subscribe → `hello` → loop.
   Remove the `authGrace` field, its init, `msgLabelAuth`, and the `msgTypeAuth`
   branch in `handleMessage` (a stray `auth` frame now falls to the default
   "Unknown message type" error). Embed `base.HandlerBase`.
3. **`messages.go`** — drop `msgTypeAuth` const and the `Token` field on
   `clientMessage`.
4. **`config.go` + tests** — remove the now-dead `RealtimeConfig.AuthGrace`
   field, its default, `SP_REALTIME_AUTH_GRACE` env parsing, and the assertions
   in `config_test.go`; drop `AuthGrace` from the integration fixture.

### Backend tests
5. `handler_test.go`: valid header → 101+hello; valid cookie → 101+hello;
   missing/invalid token → **HTTP 401, no upgrade** (assert status); header wins
   over cookie; wrong org → `4403`; first-message `auth` frame → "Unknown message
   type" error, not authentication. Update disabled/expiry tests to pre-auth.
6. `test/integration/realtime_stream_test.go`: unauthenticated dial now → HTTP
   `401` (Dial fails with a 401 response), not a grace-window `4401` close.

### Frontend (`web/dash0/src/lib/live-socket.ts` + tests)
7. Remove the `sock.onopen` in-band `{type:"auth",token}` send; drop the `token`
   arg from `connectOnce`; keep the run()-loop pre-dial refresh (re-mints the
   cookie). Update `live-socket.test.ts` (no auth frame on the wire) and the
   `live-updates-handshake.spec.ts` E2E (first client frame is `subscribe`;
   wrong-org path seeds the cookie).
