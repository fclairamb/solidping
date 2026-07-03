# Live dashboard updates via org-scoped hint events

## Problem

The dashboard only learns about changes by polling, so everything is late and the
load scales with open browser tabs rather than with actual activity:

- Dashboard page: checks refetched every 30s, results/events every 60s
  (`web/dash0/src/components/dashboard/dashboard-page.tsx`).
- Check detail: adaptive polling down to **1.5s** while a new check awaits its
  first result (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx`).
- Jobs: **2.5s** while active, 15s idle (`web/dash0/src/api/hooks.ts`).

An incident can take up to 30s to show up on a dashboard that is being actively
watched — the one moment where "live" matters most.

Distributed constraint: several API replicas (`SP_NODE_ROLE=api`) can run against
one PostgreSQL, and results are ingested elsewhere (checks-role nodes, the
heartbeat endpoint). An event produced on any node must reach dashboard
connections held by every API replica.

## Proposal

**Hint-only live events, fanned out through the existing notifier bus.**
PostgreSQL acts as a wake-up signal, never as a message relay.

### Principles

1. Events carry no data — only `{org, kinds}` hints. The client invalidates the
   matching TanStack Query caches and refetches over the normal REST API.
   Delivery is best-effort *by design*: a missed hint is corrected by the next
   hint, the slow fallback poll, or the resync on reconnect. No durable event
   log, no data-over-socket, **no schema migration**.
2. Reuse `notifier.EventNotifier` (`server/internal/notifier/`): `PgEventNotifier`
   (LISTEN/NOTIFY) on postgres/postgres-embedded, `LocalEventNotifier` on SQLite
   single-node — the same split already used for the `job.created` wakeup.

### Server

New package `server/internal/realtime/`:

- **Hub** — per-process registry of subscriber connections keyed by org uid;
  receives bus events and forwards hints to matching connections. Per-connection
  delivery state is a *dirty kind-set*, not a queue: slow clients coalesce
  naturally, memory is bounded, no backpressure machinery.
- **Coalescer** — leading-edge, per-instance, in-memory: the first hint for an
  org publishes immediately (incidents must feel instant); subsequent hints
  within the window merge into a per-org dirty set flushed by a ticker
  (default 1s). Guarantees ≤1 publish/org/sec/instance for noisy kinds
  regardless of result volume — this protects the result write path, since
  NOTIFY serializes on a global queue lock at commit (`make bench-checks` must
  not regress). No distributed rate limiter: N replicas × 1/org/sec is fine.

Bus wiring:

- Single channel `org.events`, JSON payload `{"org":"<uid>","kinds":["results",...]}`
  (far under the 8KB NOTIFY limit).
- **One shared LISTEN connection per API replica process — never per client.**
  The hub calls `notifier.Listen("org.events")` exactly once on the
  process-wide `PgEventNotifier`, whose single `pq.Listener` already
  multiplexes channels (`job.created` today) over one session reserved for
  LISTEN, with built-in reconnection (`server/internal/notifier/postgres.go`).
  Client streams never touch PostgreSQL: per-tab cost is one HTTP connection
  plus a goroutine, and PG cost stays O(replicas) — zero new connections.
  Per-client LISTEN is ruled out three ways: it burns `max_connections`,
  notification delivery is bound to the session that issued LISTEN (pooled bun
  connections can't do it), and `EventNotifier.Listen` has no unsubscribe, so
  per-client subscriptions would leak a channel per closed tab.
- Reconnect: `pq.Listener` auto-reconnects on its own, but
  `ListenerEventReconnected` is currently only logged — extend the notifier to
  surface reconnect events so the hub can broadcast `resync` to all local
  connections after a gap. The notifier's drop-on-full fan-out
  (`listenLoop`) is acceptable as-is: a dropped hint is recovered by the next
  flush, the fallback poll, or resync.

Publish sites (after the write succeeds):

| Source | Kinds | Coalescing |
|---|---|---|
| Result persisted — `handlers/heartbeat/service.go` after `SaveResultWithStatusTracking`, and the check-executor result path | `results` | coalesced |
| Check status transition, incident open/close/escalation — `handlers/incidents/service.go` (`ProcessCheckResult`, `handleFailure`, `handleSuccess`, alongside the existing `emitEvent`) | `checks`, `incidents`, `events` | immediate |
| Job lifecycle changes | `jobs` | coalesced |

Endpoint:

- `GET /api/v1/orgs/:org/events/stream` — **SSE** (`text/event-stream`), behind
  `RequireAuth` + org membership. Emits `hello` (protocol version), then `hint`
  and `resync` events; comment ping every ~25s. The server closes the stream
  when the access token expires; the client reconnects with a fresh token —
  cheap and trivially correct since hints carry no history.
- Transport rationale: SSE over WebSocket — server→client only, flows through
  plain HTTP proxies and the Vite dev proxy with no `ws: true` config, and is
  consumable by curl and the future `sp results watch`
  (`specs/done/2025/12/2025-12-24-cli.md`). The hub is transport-agnostic; a WS
  endpoint can be added later without touching bus/coalescer.

Config & metrics:

- koanf `realtime.*` / env `SP_REALTIME_*`: `enabled` (default `true`),
  `flush_interval` (1s), `ping_interval` (25s), `max_connections` guard.
  Disabled → endpoint returns 404 (same convention as `SP_PROMETHEUS_ENABLED`)
  and the dashboard silently keeps polling.
- `internal/prommetrics`: gauge for active stream connections; counters for
  hints published / delivered / coalesced. Watch label cardinality (global
  counters, not per-org labels).

### Client (dash0)

- Small fetch-based SSE reader (~50 lines, no dependency): `EventSource` cannot
  set an `Authorization` header and tokens must not go in URLs, so read the
  stream via `fetch` + `ReadableStream` with the usual Bearer header. Jittered
  reconnect backoff, capped.
- `useLiveOrgEvents(org)` mounted once in the org layout:
  - on `hint` (after ~300ms debounce) invalidate mapped keys:
    `checks` → `["checks", org]` + `["check", org]`; `results` → `["results", org]`
    plus availability / response-time / status-timeline keys;
    `incidents` → `["incidents", org]`; `events` → `["events", org]`;
    `jobs` → jobs list/detail keys.
  - on connect and on `resync` → invalidate all org-scoped keys once.
  - expose `isLive`; while connected, existing hooks stretch their
    `refetchInterval` to a lazy safety net (≥5 min). When disconnected or the
    endpoint is 404 (feature disabled), intervals stay exactly as today —
    graceful degradation is the fallback path, not an error path.
  - The 1.5s first-result poll and the 2.5s active-jobs poll rely on hints when
    live.
- No new UI in v1. If a "Live" indicator is added later it goes through the
  design reference page first.

### Out of scope

- status0 public status pages (anonymous unbounded fan-out; 30s polling of a
  cacheable endpoint stands).
- Data over the socket, durable event history, replay.
- WebSocket transport (possible later on the same hub).
- `sp results watch` CLI (future consumer of the same endpoint).

### Ops notes

- LISTEN needs a session-mode connection: document that PgBouncer transaction
  pooling is unsupported for the realtime listener (docs site + wiki).
- SQLite deployments get identical behavior through `LocalEventNotifier`
  (single process, no Postgres involved).

## Acceptance Criteria

- [ ] A check status change or incident open/close appears on an open dashboard
      within ~2s without manual refresh (Playwright E2E in `web/dash0/e2e/`
      driving a heartbeat check).
- [ ] A new check's first result appears live, without the 1.5s hot poll, while
      the stream is connected.
- [ ] Multi-replica: a hint published through one `PgEventNotifier` instance
      reaches subscribers on another (testcontainers Postgres integration
      test); the same test passes on SQLite via `LocalEventNotifier`.
- [ ] Coalescer unit test: under a burst of results for one org, the first hint
      is immediate and subsequent publishes are ≤1/sec/instance.
- [ ] Stream drop → client resumes today's polling; reconnect → exactly one
      resync invalidation.
- [ ] `SP_REALTIME_ENABLED=false` → endpoint 404s and the dashboard behaves
      exactly as today.
- [ ] Stream enforces org membership (403 per `wiki/conventions/frontend-errors.md`)
      and closes at access-token expiry; the client reconnects with a fresh
      token.
- [ ] Prometheus metrics registered (connections gauge; published / delivered /
      coalesced counters).
- [ ] `make bench-checks` shows no meaningful result-write regression with
      realtime enabled.
- [ ] Endpoint added to `server/internal/app/openapi/openapi.yaml` and
      `wiki/api-specification.md`; docs site page covers live updates and the
      PgBouncer caveat.

## Implementation Plan

1. **Notifier reconnect surface** (`server/internal/notifier/`)
   - Add `ReconnectNotifier` interface (`ReconnectEvents() <-chan struct{}`) in
     `notifier.go`; implement on `PgEventNotifier`: the `pq.Listener` callback
     fans `ListenerEventReconnected` out to registered channels (non-blocking,
     buffered 1 so bursts coalesce). `Close` closes them.
     `LocalEventNotifier` intentionally does not implement it (no transport).

2. **`server/internal/realtime/` — hints, publisher/coalescer, hub**
   - `realtime.go`: channel name `org.events`, kind constants (`results`,
     `checks`, `incidents`, `events`, `jobs`), `Hint{Org, Kinds}` JSON codec.
   - `publisher.go`: `*Publisher` (nil-safe methods so unwired callers no-op).
     Leading-edge per-org coalescer: first hint publishes immediately via
     `notifier.Notify`; hints within the flush window merge into a per-org
     dirty kind-set flushed by a ticker (default 1s). `PublishImmediate`
     bypasses the window (merging any pending kinds). `Close` stops the loop.
   - `hub.go`: per-process subscriber registry keyed by org uid. Calls
     `notifier.Listen("org.events")` exactly once; dispatch loop merges kinds
     into each matching subscriber's dirty set (bounded, no queue) and signals
     via a 1-buffered channel. If the notifier implements `ReconnectNotifier`,
     a reconnect marks every local subscriber for `resync`. Max-connections
     guard; `Close` terminates all streams (wired into server shutdown).
   - Unit tests: coalescer burst (first immediate, ≤1/sec/instance), hub
     dispatch/coalesce/resync/unsubscribe over `LocalEventNotifier`.

3. **Config + metrics**
   - `config.RealtimeConfig` (`realtime.*` / `SP_REALTIME_*`): `enabled`
     (default true), `flush_interval` (1s), `ping_interval` (25s),
     `max_connections` (default 1000). `applyRealtimeEnv` covers the
     multi-word env keys (koanf underscore quirk).
   - `prommetrics`: `solidping_realtime_connections` gauge;
     `solidping_realtime_hints_published_total`,
     `..._delivered_total`, `..._coalesced_total` counters (global, no org
     labels).

4. **SSE endpoint** — `server/internal/handlers/realtime/handler.go`
   - `GET /api/v1/orgs/:org/events/stream` behind `RequireAuth` +
     `RequireOrgAccess` (403 for non-members). Emits `hello` (protocol
     version), then `hint` / `resync` events; comment ping every
     `ping_interval`. Closes at access-token expiry (claims `exp`). Route not
     registered when `realtime.enabled=false` → 404 (same convention as
     `SP_PROMETHEUS_ENABLED`).
   - `middleware.isExcluded` gains a `/api/v1/orgs/*/events/stream` match so
     the long-lived stream bypasses the request timeout and per-IP
     rate/concurrency limits (it has its own max-connections guard).
   - `middleware/metrics.go` `statusRecorder` gets `Flush`/`Unwrap` so SSE can
     flush through the metrics wrapper.
   - Server wiring: publisher + hub built right after the event notifier in
     `NewServer`; hub closed on shutdown before `srv.Shutdown` so streams
     drain; registry exposes the publisher (`services.Realtime`).

5. **Publish sites**
   - `incidents.Service` gains a `*realtime.Publisher` (constructor param,
     nil-safe): `results` coalesced at the top of `ProcessCheckResult` (runs
     after every result persist across heartbeat/worker/remote-worker/
     emailcheck/direct paths — a superset of the spec table); `checks`
     immediate when the visible status changes; `events` + `incidents`
     immediate alongside every incident event write (emitEvent + ack/snooze
     paths).
   - Constructor threading: `server.go` (x2), `heartbeat.NewService`,
     `emailcheck.NewHandler`, `checkworker` (via services registry),
     `mcp.NewHandler`.
   - `jobsvc` gains the publisher: `jobs` coalesced on job create / status
     write / cancel / retry, when the job is org-scoped.
   - Service-level tests capture hints via `LocalEventNotifier.Listen`.

6. **Multi-replica integration test** (`server/test/integration/`)
   - Testcontainers Postgres: two `PgEventNotifier` instances on one DB; a hub
     on notifier A receives a hint published through notifier B. Same
     scenario green on `LocalEventNotifier` (single-process SQLite path).

7. **Client (dash0)**
   - `src/lib/live-events.ts`: fetch+ReadableStream SSE reader (Bearer header,
     no dependency), exported line-parser for unit tests; jittered capped
     reconnect backoff; 404/403 → permanent fallback (feature disabled).
   - `src/contexts/LiveEventsContext.tsx` + `useLiveOrgEvents`: mounted once in
     the org layout; maps hint kinds → TanStack Query keys (`checks`→checks/
     check/infinite; `results`→results/allResults/checkAvailability;
     `incidents`; `events`; `jobs`→jobsStats/backgroundJobs/checkSchedule/…)
     with ~300ms debounce; connect/resync → one full org-scoped invalidation;
     exposes `isLive`.
   - Poll stretching: while live, dashboard/check-detail/jobs hooks stretch
     `refetchInterval` to a ≥5 min safety net; the 1.5s first-result and 2.5s
     active-jobs fast polls are skipped when live. Disconnected or disabled →
     exactly today's intervals.
   - Unit tests (vitest): SSE parser; kind→key mapping.

8. **E2E (Playwright)** — `web/dash0/e2e/live-updates.spec.ts`
   - Heartbeat check via API; dashboard open; send failing heartbeat; check
     status/incident visible within ~2s without reload. Second scenario: check
     detail page shows first real result live.

9. **Docs**
   - `openapi.yaml` + `wiki/api-specification.md`: stream endpoint.
   - Docs site: `web/docs/docs/features/live-updates.md` (behavior, config
     keys, PgBouncer session-mode caveat); wiki note for the PgBouncer
     constraint.
   - `make bench-checks` sanity run to confirm no result-write regression.
