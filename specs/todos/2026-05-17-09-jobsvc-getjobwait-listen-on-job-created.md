# Wire jobsvc.GetJobWait to Listen on job.created (replace minute polling)

## Context

**Depends on** [2026-05-17-08-jobsvc-notify-via-event-notifier.md](2026-05-17-08-jobsvc-notify-via-event-notifier.md) —
Spec 08 injects `notifier.EventNotifier` into `serviceImpl` and emits `job.created`
when a new job is created. This spec closes the consumer side.

`GetJobWait` (`server/internal/jobs/jobsvc/service.go:357-391`) currently has a
standing TODO:

```go
// TODO: Add notification mechanism:
// - NOTIFY/LISTEN on postgresql
// - checkrunner's notifier logic (made generic)
ticker := time.NewTicker(time.Minute)
```

After an immediate `claimNextJob` attempt returns no rows, the runner parks on a
1-minute ticker. This means a job that becomes ready within those 60 seconds waits
up to a full minute before being picked up — on both SQLite and Postgres.

The emit side now sends a `job.created` signal through `EventNotifier`, which routes
to `LocalEventNotifier` (channels) on SQLite and `PgEventNotifier` (real
`NOTIFY job_created`) on Postgres. There is just no listener on the other end yet.

The pattern to follow already exists in the check-worker:

- `checkworker/worker.go:226` — fetcher loop calls `s.notifier.Listen("check.created")`
- `checkworker/worker.go:333` — express loop does the same
- Both use `select { case <-ch: ... case <-ticker.C: ... case <-ctx.Done(): ... }`
  with a short fallback ticker for resilience

## Goal

`GetJobWait` wakes up within milliseconds of `CreateJob` for jobs scheduled inside
the 15-minute window, on both SQLite and Postgres. The fallback poll remains for
robustness (missed signals, jobs whose scheduled_at was already in the past, etc.),
but at a longer interval.

### Honest opinion (recorded at planning time)

**Alternative considered: subscribe once at service construction, fan out internally.**
Rejected because:
1. `LocalEventNotifier.Notify` uses a non-blocking send (`local.go:52-60`). A single
   shared channel would silently drop signals under burst.
2. Per-call `Listen` is cheap (one `sync.Mutex` lock + a buffered channel allocation).
3. Matches the `checkworker` pattern exactly — lower cognitive overhead, easier review.

**Fallback interval: 5 minutes vs 1 minute.**
The wake-up channel handles the common case; the ticker is a safety net. 5 minutes
keeps latency bounded for pathological cases (cold start, lost connection on Postgres
listener reconnect, burst signal drop) without hammering the DB once a minute. The
worst-case latency is 5 minutes instead of 1 — acceptable for a background job runner
where the normal path is sub-second.

## Non-goals

- Wake-up signals for jobs scheduled more than 15 minutes ahead (still handled by
  the fallback ticker).
- Listening for `job.cancelled` or `job.updated` events.
- Multi-worker fairness changes — `claimNextJob`'s `FOR UPDATE SKIP LOCKED` (Postgres)
  and optimistic locking (SQLite) already handle the race; this spec only changes
  *when* we poll, not *how*.
- Tests for the timing behavior (hard to make deterministic without a clock
  abstraction; see
  [2026-05-17-07-clock-abstraction.md](2026-05-17-07-clock-abstraction.md)).

## Design

### `GetJobWait` replacement (`service.go:357-391`)

```go
func (s *serviceImpl) GetJobWait(ctx context.Context) (*models.Job, error) {
    ch := s.notifier.Listen(eventTypeJobCreated)
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()

    for {
        job, err := s.claimNextJob(ctx)
        if err == nil {
            return job, nil
        }

        if !errors.Is(err, sql.ErrNoRows) {
            return nil, err
        }

        select {
        case <-ctx.Done():
            return nil, ctx.Err()
        case <-ch:
            // Wake-up signal from CreateJob — try to claim immediately.
        case <-ticker.C:
            // Fallback poll: handles missed signals and jobs whose
            // scheduled_at has passed without a corresponding signal.
        }
    }
}
```

The claim attempt sits at the top of the loop so a job that landed between `Listen`
and the `select` is never missed — we always try before blocking.

The payload on the channel is discarded (any signal means "go re-check"). This mirrors
how `checkworker/worker.go:361-363` tolerates an empty `check_uid` payload.

### Channel lifecycle

Each `GetJobWait` call creates one buffered channel (`make(chan string, 1)`) via
`Listen`. On Postgres, `PgEventNotifier.Listen` registers the underlying `LISTEN
job_created` only the first time a given event type is subscribed
(`postgres.go:152-159`); subsequent calls add another in-process fan-out channel.
On SQLite, `LocalEventNotifier.Listen` appends to a slice under a lock
(`local.go:65-72`). Both are cheap and safe.

The channel is not explicitly cleaned up when `GetJobWait` returns. For
`LocalEventNotifier` this is benign — the goroutine scheduler collects it and the
`listeners` slice accumulates stale entries until `Close()`. If fan-out slice growth
becomes a concern in long-running servers, a `Unlisten` method can be added in a
follow-up.

## Files to change

### Modified files

- `server/internal/jobs/jobsvc/service.go` — replace `GetJobWait` body, remove the TODO

## Verification

**Latency test (SQLite):**

```bash
make dev-test
# In a psql / sqlite3 shell, or via the API, insert a job with scheduled_at = now+10s.
# Confirm in the server log that the runner claims it within ~1s, not within ~60s.
```

**Latency test (Postgres):**

```bash
docker-compose up -d
make dev
# Same as above. Also verify via:
psql "$DATABASE_URL" -c "LISTEN job_created;"
# Trigger a job — confirm you see the async notification AND the runner picks it up fast.
```

**Fallback resilience:**

Temporarily rename the constant `eventTypeJobCreated` in `service.go` to `"job.created.wrong"`
so `Listen` and `Notify` no longer match. Create a job. Confirm the runner picks it
up within 5 minutes (fallback ticker). Revert.

**Cancellation / clean shutdown:**

Start `make dev-test`, let the runner park in `GetJobWait`, then kill the server
(Ctrl-C). Confirm clean shutdown with no goroutine-related panics or leaks in the log.

**Build / lint / test:**

```bash
make build
make lint
make test
```

## Risk log

| Risk | Mitigation |
|---|---|
| Channel not cleaned up → `listeners` slice grows unboundedly | Each entry is a single channel pointer (~8 bytes); in practice a handful of concurrent `GetJobWait` callers keep the list tiny. Add `Unlisten` in a follow-up if needed. |
| Signal dropped under burst → job waits 5 min | `claimNextJob` at loop top plus fallback ticker; worst case is 5 minutes, same class of latency as the old code at 1 minute. |
| Postgres listener reconnect gap → missed signals | `PgEventNotifier.listenLoop` handles `pq.ListenerEventReconnected` transparently; the fallback ticker covers the reconnect window. |
