# Realtime v2: WebSocket transport with per-entity subscriptions

Supersedes the shipped SSE design
(`specs/done/2026/07/2026-07-02-01-live-dashboard-updates-org-scoped-hint-events.md`).
That implementation is merged on the batch branch but **has not reached
`main`**, so this is a clean replacement — no deprecation window, no
back-compat shims. The SSE endpoint, its client reader, and their docs are
removed as part of this spec.

## Problem

Realtime v1 broadcasts every org hint to every open dashboard of that org.
That is fine for small orgs, but it has no notion of *interest*:

- A user staring at one check's detail page still receives (and refetches on)
  hints caused by the org's 499 other checks. Hint kinds narrow *what* to
  invalidate, never *which entity* — `results` invalidates every
  results-family query of the org (`HINT_KIND_QUERY_ROOTS` in
  `web/dash0/src/contexts/LiveEventsContext.tsx`).
- The server cannot skip work for orgs/entities nobody is watching at a finer
  grain than "org has ≥1 stream".
- SSE is one-directional, so the client has no way to express interest at all.

v2 inverts the default: **a connection receives nothing until it subscribes**,
and updates are scoped to the subscribed entity. That requires client→server
messages, which is what justifies switching the transport to WebSocket.

## What stays (unchanged v1 machinery)

The transport and routing change; the architecture does not:

- Hint-only events (no data over the socket), REST remains the source of
  truth; missed messages are recovered by ack'd resubscribe, `resync`, or the
  lazy fallback poll.
- The notifier bus: single `org.events` LISTEN per process
  (`realtime.ChannelOrgEvents`), `notifier.ReconnectNotifier` resync
  broadcasts, `LocalEventNotifier` on SQLite. No schema, no migration.
- `realtime.Publisher` leading-edge coalescing (first hint immediate, then
  ≤1 publish/org/`flush_interval`/instance) and `PublishImmediate` for
  incident/status transitions.
- Per-subscriber **dirty-set, not queue** delivery (`realtime.Subscriber`);
  slow clients coalesce, memory bounded.
- `SP_REALTIME_*` config block, Prometheus counters, middleware exclusions
  (timeout/ratelimit/metrics) for the long-lived endpoint, PgBouncer
  session-mode caveat in the docs.
- Client philosophy: TanStack Query invalidation + 300ms debounce +
  `stretchWhileLive` lazy polling (`LIVE_LAZY_POLL_MS`) as the always-working
  fallback. `SP_REALTIME_ENABLED=false` → dashboards behave exactly as
  pre-realtime.

## Protocol (v2)

Endpoint: `GET /api/v1/orgs/:org/events/ws` (WebSocket upgrade). One socket
per org layout per tab, multiplexing all subscriptions. Messages are
single-frame JSON text, camelCase, `uid` everywhere (repo API convention —
never `id`). Unknown server→client message types must be ignored by the
client; unknown client→server types get an `error` reply (forward compat).

### Handshake

1. Client connects. If the upgrade request carries a valid `Authorization`
   header or `access_token` cookie (CLI, tests, curl/websocat), it is
   authenticated immediately. Browsers cannot set headers on WebSocket, so:
2. Within `SP_REALTIME_AUTH_GRACE` (default 5s) the client must send
   `{"type":"auth","token":"<jwt>"}`. Tokens never go in the URL.
3. Server validates the token (same `authService.ValidateToken` + org access
   path as `middleware.RequireAuth`/`RequireOrgAccess`, executed in-handler —
   see "Route registration" below) and replies
   `{"type":"hello","protocol":2}`. Any message other than `auth` before
   authentication → close `4401`.

### Client → server

| Message | Purpose |
|---|---|
| `{"type":"auth","token":"..."}` | Authenticate (browsers). First message. |
| `{"type":"subscribe","entity":"check","uid":"<uuid>"}` | Watch one check (detail pages). |
| `{"type":"subscribe","entity":"checks"}` | Watch the org's check collection: membership + any check's status/results (dashboard, lists). |
| `{"type":"subscribe","entity":"incidents"}` / `"events"` / `"jobs"` | Watch the org-level collection. |
| `{"type":"unsubscribe","entity":...,"uid":...}` | Drop a subscription (route change). |

Scopes: `check`+`uid` is the only per-entity scope in v2; `checks`,
`incidents`, `events`, `jobs` are org-collection scopes (org implied by the
connection). Collections exist because the dashboard, incident list, events
feed, and jobs pages display org-wide data — strictly per-check subscriptions
would leave them dark and would never learn about newly created checks.

### Server → client

| Message | Meaning |
|---|---|
| `{"type":"hello","protocol":2}` | Auth accepted; protocol version. |
| `{"type":"subscribed","entity":...,"uid":...}` | Subscription ack. The client marks the scope live (and stretches its polls) **only after this ack**. |
| `{"type":"update","entity":"check","uid":"...","kinds":["results"]}` | The subscribed check changed. `kinds` ⊂ {results, checks, incidents} narrows which query roots to invalidate; the client treats a missing/empty `kinds` as "all". |
| `{"type":"update","entity":"checks"}` (etc.) | The subscribed collection changed. |
| `{"type":"resync"}` | Bus transport gap (notifier reconnected): invalidate **all currently subscribed scopes** once. |
| `{"type":"error","code":"...","title":"...","entity":...,"uid":...}` | Per-message failure; echoes the offending scope. Reuses REST error codes: `NOT_FOUND` (check not in this org), `VALIDATION_ERROR` (malformed), `CONCURRENCY_LIMITED` (subscription cap). A duplicate subscribe is **not** an error — it is idempotent and returns the normal `subscribed` ack (the registry replays scopes after reconnect, so duplicates are routine). Errors do not close the socket. |

### Close codes

WebSocket upgrade failures are invisible to browser JS (only a generic 1006),
so terminal conditions are signaled by **accepting the socket and closing
with an app code**, preserving v1's clean `onDisabled` semantics:

| Code | Condition | Client reaction |
|---|---|---|
| `4401` | Auth missing/invalid/timeout, or **access-token expiry** mid-connection (v1 behavior kept: server closes at `claims.ExpiresAt`, client reconnects with a fresh token and resubscribes) | Reconnect with backoff (fresh token from storage). |
| `4403` | Not an org member | Permanent stop, silent fallback to polling. |
| `4404` | `SP_REALTIME_ENABLED=false` | Permanent stop, silent fallback to polling. |
| `1012` | Server shutdown / hub closed | Reconnect with backoff. |

Keepalive: transport-level ping every `SP_REALTIME_PING_INTERVAL` (default
25s) with a pong deadline; a dead peer is detected and reaped (an
improvement over SSE, which only detected dead clients on write).

## Server changes

Dependency: `github.com/coder/websocket` — already in go.mod via the
WebSocket *checker* (`internal/checkers/checkwebsocket`).

### `server/internal/realtime/realtime.go` — bus payload v2

Extend `Hint` with check attribution:

```json
{"org":"<uid>","kinds":["results","incidents"],"checkUids":["u1","u2"]}
```

- `checkUids` lists the checks the check-attributable kinds (`results`,
  `checks`, `incidents`) apply to. `events`/`jobs` remain org-level.
- **Storm collapse**: when a flush window accumulates more than
  `CollapseCheckUids = 64` distinct uids for an org, the publisher replaces
  the list with `["*"]`. The payload stays far under the 8KB NOTIFY limit
  (~200 uuid strings would approach it), and the mass-outage case — the one
  that produces such bursts — degrades to "every watcher of this org
  refetches once", which is what they would do anyway.
- Accepted imprecision: kinds and uids are merged per window, so a window
  mixing a result for check A and an incident for check B yields
  `kinds:[results,incidents] × uids:[A,B]` — subscribers over-invalidate
  within that pair. Hint semantics make this harmless; do not build a
  per-uid kind map.

### `server/internal/realtime/publisher.go`

- `Publish`/`PublishImmediate` gain check attribution:
  `Publish(ctx, orgUID, checkUID string, kinds ...Kind)` with `checkUID == ""`
  meaning org-level (events, jobs). Pending state becomes
  `map[orgUID]{kinds map[Kind]struct{}, checkUids map[string]struct{}, collapsed bool}`.
- Call sites already publish from the result, incident, and job write paths
  (v1, commit `dafe9bd2`); they all know their check uid — thread it through.
  Coalescing, leading edge, pruning, nil-receiver no-op: unchanged.

### `server/internal/realtime/hub.go`

- `Subscriber` gains a subscription set and scope-keyed dirty state:
  `subscriptions map[scopeKey]struct{}`, `dirty map[scopeKey]map[Kind]struct{}`
  (`scopeKey` = `{entity, uid}`). `Take()` drains `[]ScopedUpdate` plus the
  existing `resync`/`closed` flags. The 1-buffered `signal` wake-up pattern is
  unchanged.
- `Subscribe(orgUID)` keeps the connection-level `maxConns` guard;
  new `sub.AddScope(scope)` / `sub.RemoveScope(scope)` enforce
  `SP_REALTIME_MAX_SUBSCRIPTIONS_PER_CONNECTION` (default 512).
- `dispatch()` routes a decoded v2 hint to: collection subscribers whose
  entity matches a hint kind family, and `check` subscribers whose uid is in
  `checkUids` (or on `["*"]`, every `check` subscriber of that org).
  A connection with zero scopes receives nothing — the default-silent
  guarantee lives here and gets a dedicated test.
- `broadcastResync()` unchanged (per-connection resync flag; the client maps
  it to its own subscribed scopes).

### `server/internal/handlers/realtimews/` — replaces `handlers/realtimestream/`

- Accept the upgrade (`websocket.Accept`, compression optional off), then run
  the handshake state machine: pre-authenticated via header/cookie, or
  `auth` message within the grace window; validate org access; then the
  read-loop (subscribe/unsubscribe with per-scope validation) and write-loop
  (drain `sub.Take()` on `sub.Signal()`, one `update` frame per dirty scope)
  plus the ping ticker and the token-expiry timer
  (`tokenExpiryTimer` logic carries over from
  `handlers/realtimestream/handler.go:137` — claims now come from the
  in-handler validation rather than middleware context).
- **Subscribe validation**: `check` scope → one org-scoped existence lookup
  via the checks service (`NOT_FOUND` on miss — org-scoped, so no cross-org
  existence leak); cache positives per connection. Collections need no
  lookup.
- **Route registration** (`internal/app/server.go`): the route is registered
  **always** (even when disabled → accept + close `4404`) and **outside**
  `RequireAuth`/`RequireOrgAccess`, because browsers cannot present
  credentials at upgrade time; the handler performs the exact same
  validation in-band and closes `4401`/`4403` otherwise. Carry over the v1
  middleware exclusions (timeout, rate-limit, HTTP metrics) to the new path.
- Remove `handlers/realtimestream/` (handler + tests) and the
  `/events/stream` route.

### Config (`server/internal/config/config.go`)

Existing `realtime.*` keys unchanged. New keys — **remember the koanf
multi-word env quirk: add them to the manual `SP_*` env reader** like the
existing `flush_interval`:

| Key | Env | Default |
|---|---|---|
| `realtime.auth_grace` | `SP_REALTIME_AUTH_GRACE` | `5s` |
| `realtime.max_subscriptions_per_connection` | `SP_REALTIME_MAX_SUBSCRIPTIONS_PER_CONNECTION` | `512` |

### Metrics (`server/internal/prommetrics/metrics.go`)

Keep `RealtimeConnections`, `RealtimeHintsPublished/Delivered/Coalesced`; add
`RealtimeSubscriptions` (gauge) and `RealtimeMessagesReceived`
(counter, label `type`). No per-org labels (cardinality).

## Client changes (dash0)

### `web/dash0/src/lib/live-socket.ts` — replaces `live-events.ts`

WebSocket client keeping the good parts of `connectLiveEvents`: jittered
capped backoff (`backoffDelay`), token re-read from storage per attempt,
`waitForApiQuiet` gating (Playwright `networkidle` still needs it), and the
callback surface — now: `onOpen` (after `hello`), `onUpdate(scope, kinds)`,
`onResync`, `onDisconnected`, `onDisabled` (close `4403`/`4404`), plus
`send(subscribe/unsubscribe)`. Sends `auth` as the first message.

### `web/dash0/src/contexts/LiveEventsContext.tsx` — subscription registry

- Provider (still mounted once in `routes/orgs/$org.tsx`) owns the socket and
  a **refcounted subscription registry**: components declare interest through
  a new hook, and the registry sends `subscribe`/`unsubscribe` on 0↔1
  refcount transitions and **replays every active scope after each
  reconnect**, then invalidates those scopes (no more whole-org
  `invalidateAllForOrg` on reconnect — scope-accurate now).
- New hook: `useLiveSubscription(scope, queryRoots?)` — e.g. the check detail
  page calls `useLiveSubscription({entity:"check", uid: checkUid})`; the
  dashboard calls it for `checks`, `incidents`, `events`. Each scope carries
  its query-root mapping (defaults derived from today's
  `HINT_KIND_QUERY_ROOTS`); explicit roots per scope avoid fragile
  key-predicate guessing (results query keys hold the check uid inside an
  options object, so `key.includes(uid)` would not match).
- Live state becomes **per-scope**: a scope is live only between its
  `subscribed` ack and disconnect. `useLiveStatus()`/`stretchWhileLive`
  keep their signatures (global "socket open" for the coarse consumers), and
  per-scope liveness gates each page's poll stretching — a rejected
  subscription must keep polling at today's rates. Keep the 300ms hint
  debounce and `LIVE_LAZY_POLL_MS`.
- Wire the existing consumers: `components/dashboard/dashboard-page.tsx`
  (collections), `routes/orgs/$org/checks.$checkUid.index.tsx` (its check +
  `incidents`), jobs pages (`jobs`).

### Vite

Both proxies (`web/dash0/vite.config.ts`, dev use only) need a WebSocket
entry: `"/api/v1/orgs": { target: "http://localhost:4000", ws: true, ... }`
or a dedicated `/api` `ws: true` flag — verify HMR websockets are unaffected.

## Docs & specs to update

- `web/docs/docs/features/live-updates.md`: rewrite for the WS protocol
  (message table above, close codes, websocat example replacing curl).
- `wiki/api-specification.md`: replace the stream endpoint entry.
- OpenAPI (`server/internal/app/openapi/openapi.yaml`): remove
  `/events/stream`; OpenAPI 3 cannot model WebSocket — document the endpoint
  in prose/docs instead (keep a pointer in the API spec wiki page).
- `web/docs/docs/configuration/database.md` PgBouncer note: unchanged
  (LISTEN still requires session mode).

## Out of scope

- status0 / public status pages (anonymous fan-out is a different problem).
- Data over the socket, durable event history, replay, message ids.
- Per-user/per-label filtered subscriptions beyond `check` + collections.
- `sp results watch` CLI (future consumer; protocol is documented for it).

## Acceptance criteria

- [ ] **Default silent**: an authenticated socket with zero subscriptions
      receives no `update` frames while the org's checks run (integration
      test with active heartbeat traffic).
- [ ] Subscribing to `{"entity":"check","uid":X}` delivers updates for X only:
      activity on another check of the same org produces no frame (hub unit
      test + integration test).
- [ ] Collection subscriptions keep the dashboard live: check
      creation/deletion and status transitions reflect within ~2s with only
      `checks`/`incidents` subscribed (Playwright E2E, rewritten
      `web/dash0/e2e/live-updates.spec.ts`).
- [ ] Storm collapse: >64 distinct check uids in one flush window publish as
      `["*"]` and reach both collection and per-check subscribers (publisher
      + hub tests).
- [ ] Handshake: no `auth` within the grace window → close `4401`; subscribe
      before auth → close `4401`; foreign-org check uid → `error NOT_FOUND`,
      socket stays open; subscription cap → `error CONCURRENCY_LIMITED`.
- [ ] Token expiry closes with `4401`; the client reconnects, replays its
      subscriptions, and invalidates exactly the subscribed scopes (client
      unit test on the registry + E2E).
- [ ] `SP_REALTIME_ENABLED=false` → accept + close `4404`; dashboards behave
      exactly as pre-realtime (poll intervals unstretched).
- [ ] Multi-replica: v2 hints published through one `PgEventNotifier` reach
      per-check subscribers on another instance (extend
      `realtime/multireplica_test.go`); same coverage on `LocalEventNotifier`.
- [ ] Poll stretching is gated on the `subscribed` ack per scope (client unit
      test: rejected subscription keeps base intervals).
- [ ] SSE fully removed: `handlers/realtimestream/`, `lib/live-events.ts`,
      `/events/stream` route, OpenAPI path, and docs references are gone;
      `rg "events/stream"` finds nothing outside specs/done.
- [ ] `make bench-checks` shows no meaningful result-write regression
      (publisher signature change only).
- [ ] Docs and `wiki/api-specification.md` describe the v2 protocol; lint,
      backend tests, dash0 unit tests, and E2E green.

## Implementation Plan

Order chosen so every commit leaves `main`/the batch branch buildable: bus
payload → hub → new WS handler alongside the old SSE handler → route swap →
config/metrics → frontend client → frontend registry/hook → wire consumers →
Vite → remove SSE → docs → tests/QA pass.

1. **Bus payload v2** (`realtime/realtime.go`): add `CheckUids []string` to
   `Hint`; `EncodeHint`/`DecodeHint` gain a check-uid set param; add
   `CollapseCheckUids = 64` and the `["*"]` collapse rule as a small pure
   helper (`collapseCheckUids`) unit-testable in isolation. Update existing
   hub/publisher callers in this same package.

2. **Publisher check attribution** (`realtime/publisher.go`): change
   `Publish`/`PublishImmediate` signature to
   `(ctx, orgUID, checkUID string, kinds ...Kind)`; pending state becomes
   `{kinds, checkUids, collapsed}` per org. Thread `checkUID` (or `""`) through
   every call site: `incidents/service.go` (`ProcessCheckResult` →
   `check.UID`; `publishStatusHint` → `check.UID`; `emitEvent` and the 4
   ack/snooze/unsnooze sites → `incident.CheckUID`), `heartbeat/service.go`
   (`org.UID` result path → the heartbeat's check uid), `jobsvc/service.go`
   (`checkUID=""`, org-level). Update `publisher_test.go` call sites and add
   the storm-collapse test (>64 uids → `["*"]`).

3. **Hub v2 dispatch** (`realtime/hub.go`): `Subscriber` gains
   `subscriptions map[scopeKey]struct{}` + `dirty map[scopeKey]map[Kind]struct{}`;
   `scopeKey{entity, uid}`. Add `AddScope`/`RemoveScope` (enforce
   `maxSubscriptions`, return a sentinel error for the cap). `Take()` returns
   `[]ScopedUpdate` (scope + sorted kinds) plus resync/closed. `dispatch()`
   routes a decoded hint to: (a) subscribers on the matching collection scope
   (`checks`/`incidents`/`events`/`jobs`) when the hint carries that kind, (b)
   `check`-scoped subscribers whose uid is in `checkUids` or when
   `checkUids == ["*"]`. Zero-scope subscribers get nothing. Rewrite
   `hub_test.go` for scope-based dispatch (keep the org-isolation,
   slow-subscriber, resync, close, max-conns-guard shapes) and add: per-check
   isolation, default-silent (zero scopes → no dispatch), storm-collapse
   reaching both a collection and a per-check subscriber, subscription-cap
   error. Extend `multireplica_test.go` for v2 hints (`checkUids`) on both
   `PgEventNotifier` and `LocalEventNotifier`.

4. **Config** (`config/config.go`): add `AuthGrace time.Duration` and
   `MaxSubscriptionsPerConnection int` to `RealtimeConfig`; defaults `5s` /
   `512`; extend `applyRealtimeEnv` for `SP_REALTIME_AUTH_GRACE` /
   `SP_REALTIME_MAX_SUBSCRIPTIONS_PER_CONNECTION` (multi-word env quirk).

5. **Metrics** (`prommetrics/metrics.go`): add `RealtimeSubscriptions` (gauge)
   and `RealtimeMessagesReceived` (counter, label `type`); register in
   `allCollectors`.

6. **New handler package** `handlers/realtimews/`: `handler.go` with
   `NewHandler(hub, authService, dbService, cfg)`; `Serve(w, req)` — accept
   the upgrade unconditionally (even disabled/unauthenticated) via
   `websocket.Accept`, since browsers only see close codes, not HTTP status;
   then:
   - pre-auth check (header/cookie via the same token-extraction helper as
     `middleware.RequireAuth`, exported or duplicated locally to avoid an
     import cycle) → validate token + org access inline using
     `authService.ValidateToken` + `dbService.GetOrganizationBySlug` +
     membership check (mirrors `RequireOrgAccess`); on success send `hello`
     and proceed to the read loop already authenticated;
   - else start a grace-window timer (`cfg.Realtime.AuthGrace`) and read
     messages: first non-`auth` message before auth → close `4401`; `auth`
     message → validate token, same org-access check → `hello` or close
     `4401`/`4403`; grace timeout → close `4401`.
   - post-auth read loop: `subscribe`/`unsubscribe` messages validated
     per-scope (`check`+uid → `dbService.GetCheck(ctx, org.UID, uid)`,
     `NOT_FOUND` on miss, cache positive lookups in a per-connection
     `map[string]struct{}`; collections need no lookup) → `hub.Subscribe`
     scope add (cap → `error CONCURRENCY_LIMITED`, socket stays open) →
     `subscribed` ack (duplicate = idempotent ack, not an error); malformed
     JSON / unknown type → `error VALIDATION_ERROR`, socket stays open.
   - write loop: goroutine draining `sub.Signal()` → `sub.Take()` → one
     `update` frame per scope in the drained batch (`resync` frame first if
     set).
   - ping ticker (`SP_REALTIME_PING_INTERVAL`) using `conn.Ping`; a failed
     pong within the library's internal timeout surfaces as a `Ping` error →
     close `1012`/abort. Token-expiry timer carried over from
     `realtimestream/handler.go:137` (`tokenExpiryTimer`, adapted to the
     locally-validated claims) → close `4401` at expiry.
   - `SP_REALTIME_ENABLED=false` stub path: accept + immediately close
     `4404` (registered unconditionally per the spec, mirroring today's JSON
     404 stub intent but as a close code since it's a WS route now).
   Write `handler_test.go` using a real `httptest.Server` + `coder/websocket`
   client dials (mirrors the old SSE fixture shape) covering every acceptance
   criterion: default-silent, per-check isolation (integration level),
   handshake edge cases (no-auth timeout, subscribe-before-auth, foreign-org
   uid, subscription cap), token-expiry close+reconnect, disabled-flag close
   code.

7. **Route registration** (`app/server.go`): replace the
   `realtimestream`-backed `/events/stream` block with
   `/orgs/:org/events/ws` registered **outside** `RequireAuth`/
   `RequireOrgAccess` (in-handler auth), still carrying the timeout/
   ratelimit/metrics middleware exclusions — extend `isExcluded`'s suffix
   constant/logic in `middleware/ratelimit.go` and `middleware/timeout.go`
   from `/events/stream` to `/events/ws` (keep it a single shared constant).
   Remove the old `realtimestream` import, handler construction, and the
   JSON-404 stub branch (the new handler self-handles the disabled case).

8. **Remove `handlers/realtimestream/`**: delete the package (handler.go +
   handler_test.go) once the new handler covers its test surface.

9. **Frontend: `lib/live-socket.ts`** (new, replaces `live-events.ts`):
   WebSocket client — `connectLiveSocket(org, callbacks)` returning a
   disposer + `send()`. Keep `backoffDelay`, `waitForApiQuiet`, token re-read
   per attempt (now sent as the first `auth` frame, not a header). Parse
   `hello`/`subscribed`/`update`/`resync`/`error` frames; unknown types
   ignored. Close code 4403/4404 → `onDisabled`; else → `onDisconnected` +
   reconnect. Export a small message-encoder for subscribe/unsubscribe so the
   registry doesn't hand-roll JSON. Delete `live-events.ts` +
   `live-events.test.ts`, write `live-socket.test.ts` covering backoff,
   frame parsing, and the auth-first-message contract.

10. **Frontend: `contexts/LiveEventsContext.tsx` registry rewrite**: keep
    `LiveEventsProvider`, `HINT_KIND_QUERY_ROOTS`-equivalent per-scope root
    mapping, `LIVE_LAZY_POLL_MS`. Add refcounted scope registry
    (`Map<scopeKey, {count, queryRoots, live}>`); `useLiveSubscription(scope,
    queryRoots?)` hook subscribes on mount (0→1 refcount sends `subscribe`),
    unsubscribes on unmount (1→0 sends `unsubscribe`); replays all active
    scopes after reconnect (`onOpen`) and invalidates them (scope-accurate,
    replacing `invalidateAllForOrg`); `onResync` still invalidates every
    subscribed scope. `useLiveStatus()` keeps today's global "socket open"
    signature for coarse consumers (ping ticker state); add
    `useScopeLive(scope)` returning true only between that scope's
    `subscribed` ack and disconnect, for per-scope poll-stretch gating.
    Rewrite `LiveEventsContext.test.ts` for the registry (refcounting,
    replay-on-reconnect, poll-stretch gated on ack).

11. **Wire consumers**: `dashboard-page.tsx` calls
    `useLiveSubscription({entity:"checks"})`,
    `useLiveSubscription({entity:"incidents"})`,
    `useLiveSubscription({entity:"events"})` and gates its `stretchWhileLive`
    calls on the matching `useScopeLive`. `checks.$checkUid.index.tsx` calls
    `useLiveSubscription({entity:"check", uid: checkUid})` +
    `useLiveSubscription({entity:"incidents"})`. Jobs pages/hooks
    (`api/hooks.ts` jobs section) call `useLiveSubscription({entity:"jobs"})`
    once at the page level and gate `jobsAdaptiveInterval` on
    `useScopeLive({entity:"jobs"})` instead of the global `useLiveStatus`.

12. **Vite** (`vite.config.ts`): add `ws: true` to the `/api` proxy entry;
    manually verify (via `make dev`) that Vite's own HMR websocket (separate
    port/path) still connects.

13. **Docs**: rewrite `web/docs/docs/features/live-updates.md` (message
    table, close codes, `websocat` example). Update
    `wiki/api-specification.md` stream entry → WS entry. Remove
    `/events/stream` from `openapi.yaml`, add a short prose note pointing at
    the wiki (OpenAPI 3 can't model WS).

14. **E2E**: rewrite `web/dash0/e2e/live-updates.spec.ts` — wait for the
    `subscribed` ack (or the WS upgrade response) instead of the old
    `/events/stream` 200 response; same two scenarios (incident opens live on
    dashboard, first result appears live on detail page without the hot
    poll).

15. **Sweep + QA**: `rg "events/stream"` (expect zero outside
    `specs/done`), `make bench-checks`, full QA matrix (step D of the runbook).
