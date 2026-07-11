# EventNotifier channel leak: GetJobWait leaks one listener per job and eventually silences the realtime WS

## Problem

Diagnosed live on the k8xp dev pod on 2026-07-11 (prod runs the same code and
is equally affected): after ~1h of uptime the realtime WebSocket goes
**permanently silent** — `hello` and `subscribed` acks still work (they are
hub-local), but no `update` frame is ever delivered again, for any org or
check, with **zero log lines**.

Root cause is a listener-channel leak on the EventNotifier bus:

1. **The leak.** `GetJobWait`
   (`server/internal/jobs/jobsvc/service.go:508`) calls
   `s.notifier.Listen(eventTypeJobCreated)` on **every call**, and the
   `EventNotifier` interface (`server/internal/notifier/notifier.go:13`) has
   no Unlisten — both `LocalEventNotifier.Listen`
   (`server/internal/notifier/local.go:65`) and `PgEventNotifier.Listen`
   (`server/internal/notifier/postgres.go:152`) append a fresh 16-slot
   channel to `n.listeners[eventType]` forever. Job runners
   (`server/internal/jobs/jobworker/worker.go:146`) call `GetJobWait` in a
   loop, so **one channel leaks per processed job**. (`LocalEventNotifier`'s
   own `ListenerCount` doc comment already flags this leak as the
   memory-analysis signal.)

2. **The collapse (Postgres).** `PgEventNotifier.listenLoop`
   (`server/internal/notifier/postgres.go:88`) forwards every incoming
   notification to ALL registered channels of its event type, so
   per-notification cost grows linearly with the leak. Once inflow (NOTIFYs
   from all instances sharing the DB) exceeds drain rate, the single
   `pq.Listener` FIFO backs up: lib/pq's dispatch blocks on the full
   `Notify` channel, and `org_events` hints (the realtime hub's channel)
   queue behind hours of `job.created` noise. Observed:
   `pg_notification_queue_usage()` ≈ 0.117% of 8GB (~10MB backlog);
   `/metrics` showed 103,578 hints published vs **47** delivered over 9h.
   Because the LISTEN connection never drops, the reconnect → resync path
   (`hub.broadcastResync`) never fires and nothing is logged — the failure
   is completely silent.

3. **Secondary leak.** `listenLoop`'s 90s keepalive ticker spawns
   `go n.listener.Ping()` (`server/internal/notifier/postgres.go:122`); once
   the pipeline is stuck, the first ping blocks forever *holding* the
   pq listener mutex and every subsequent ping goroutine blocks on it —
   the goroutine dump showed **364 stuck goroutines** in 9h, exactly one per
   tick.

## Proposal

1. **Stop the leak.** Either:
   - add an unsubscribe mechanism to `EventNotifier` (e.g.
     `Listen` returns a channel plus a cancel/`Unlisten` func, or a
     `Subscription` handle with `Close()`), and have `GetJobWait` defer it;
     or
   - subscribe **once per service/runner** instead of per call — e.g. a
     shared wakeup channel created in `jobsvc.NewService` (dropped wakeups
     coalesce by design, so one channel per consumer is sufficient).

   Audit the other `Listen` call sites (`checkworker/worker.go:394,524`,
   `realtime.NewHub`) — those subscribe once per long-lived loop and are
   fine, but they should keep working unchanged under the new API.

2. **Make the Ping keepalive non-stacking.** Skip the tick if the previous
   ping is still in flight (in-flight flag), and/or run the ping with a
   timeout so a stuck pipeline surfaces as a logged warning instead of an
   unbounded goroutine pile-up.

3. **Observability.** Warn (rate-limited) and/or export a metric when a
   listener slice grows abnormally — `ListenerCount` is already exposed on
   `/api/mgmt/memory` and via `sizes.EventListeners`
   (`server/internal/app/server.go:381`); a Prometheus gauge would let this
   be alerted on.

4. **Regression test.** Assert `ListenerCount` stays bounded across
   repeated `GetJobWait` calls (table-driven, `testify/require`,
   `t.Parallel()`), for both `LocalEventNotifier` and `PgEventNotifier`
   (testcontainers for the Postgres path, mirroring
   `server/internal/notifier/postgres_test.go`).

## Notes

- Immediate mitigation applied on the dev pod on 2026-07-11: process restart
  (clears the backlog); the leak regrows with job volume, so the code fix is
  the real remedy.
- Diagnostic recipe that pinned this down, for posterity:
  `pg_notification_queue_usage()` > 0, published-vs-delivered hint counters
  on `/metrics`, and a goroutine dump via
  `kubectl debug <pod> --image=busybox:1.36 --target=backend -q -- sh -c
  'kill -QUIT 1'` + `kubectl logs --previous`.

## Implementation Plan

Chosen fix for requirement 1: **add an `Unlisten` method to the
`EventNotifier` interface** (rather than change `Listen`'s signature). This is
the least-churn option — every existing `Listen` call site keeps working
**unchanged**, and only the per-call subscriber (`GetJobWait`) has to deregister.

1. **Stop the leak (interface + both notifiers).**
   - Add `Unlisten(eventType string, ch <-chan string)` to the `EventNotifier`
     interface (`notifier.go`).
   - Implement it on `LocalEventNotifier` and `PgEventNotifier`: take the write
     lock, find the channel in `n.listeners[eventType]` (`chan string` is
     comparable to the `<-chan string` handed back by `Listen`), swap-remove it
     (nil the vacated slot to release the reference), and return. Deregister
     only — do **not** close the channel, so there is no double-close race with
     `Close()` (which closes every remaining channel at shutdown).
   - `GetJobWait` (`jobsvc/service.go`): `defer s.notifier.Unlisten(eventTypeJobCreated, wakeup)`
     right after `Listen`, so every call cleans up its own subscription.
   - Audit confirms the other `Listen` call sites subscribe once per long-lived
     loop and are fine as-is: `checkworker/worker.go:394,524`,
     `realtime.NewHub` (`hub.go:99`) — unchanged.

2. **Non-stacking Ping keepalive (`postgres.go` listenLoop).** Guard the 90s
   tick with a local `atomic.Bool` in-flight flag: only spawn `go n.listener.Ping()`
   when no previous ping is still running; if one is, skip the tick and log a
   rate-limited warning that the pipeline appears stuck. Bounds the ping
   goroutines to at most one instead of one-per-tick (the 364-goroutine pile-up).

3. **Observability — rate-limited growth warning.** In both notifiers' `Listen`,
   after appending, warn (at most once/minute, guarded by a `lastGrowthWarn`
   field under the same write lock) when a single event type's listener slice
   crosses an abnormal threshold (`listenerGrowthWarnThreshold = 1000`). The
   Prometheus gauge `solidping_event_listeners` already exists
   (`prommetrics.SubsystemSizes.EventListeners`, wired at `server.go:381`), so no
   new metric is needed — the fix keeps that gauge bounded and the warning gives
   early log-side detection if a future leak is introduced.

4. **Regression tests.**
   - `notifier` package: assert `ListenerCount()` returns to baseline across
     repeated `Listen`/`Unlisten` cycles, for both `LocalEventNotifier` and
     `PgEventNotifier` (embedded-postgres subtest, mirroring
     `postgres_test.go`), plus an `Unlisten`-of-unknown-channel no-op check.
   - `jobsvc` package: seed claimable pending jobs, call `GetJobWait` in a loop
     against a `LocalEventNotifier`, and assert `notifier.ListenerCount` stays
     bounded (returns to 0) after each claim — the end-to-end regression for the
     original leak.
