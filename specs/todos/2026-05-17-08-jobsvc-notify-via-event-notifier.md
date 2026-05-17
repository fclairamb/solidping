# Route jobsvc NOTIFY through EventNotifier (silences SQLite WARN)

## Context

Every job creation under SQLite emits a spurious WARN:

```
time=2026-05-17T23:08:21.064+02:00 level=WARN msg="SQL query failed"
duration=6.667µs operation=NOTIFY
error="SQL logic error: near \"NOTIFY\": syntax error (1)"
```

Two pieces of code cooperate to produce it:

**1. The bad call site** — `server/internal/jobs/jobsvc/service.go:218-223`, inside
`createNewJob`. When the new job is scheduled within 15 minutes it fires raw Postgres
SQL against `*bun.DB` regardless of the driver:

```go
// Send NOTIFY signal to wake up job runners if job is scheduled within 15 minutes
// This is PostgreSQL-specific and will be ignored on SQLite (polling fallback is used)
if time.Until(job.ScheduledAt) <= 15*time.Minute {
    _, _ = s.db.ExecContext(ctx, "NOTIFY jobs")
    // Ignore errors - NOTIFY is not available on SQLite, which uses polling instead
}
```

The `_, _ =` pattern silences the returned error, but **bun's query hook fires before the
caller can discard anything**.

**2. The WARN amplifier** — `server/internal/db/sloghook/hook.go:45-71`,
`(*QueryHook).AfterQuery`. It logs every failed query at WARN regardless of what the
caller does with the error. bun parses the first token (`NOTIFY`) as the operation name,
producing `operation=NOTIFY` in the structured log.

### The fix already exists — it just isn't wired

`server/internal/notifier/` already ships a database-agnostic event-notifier abstraction:

| Implementation | File | Mechanism |
|---|---|---|
| `LocalEventNotifier` | `notifier/local.go` | Go channels — works on SQLite and any other backend |
| `PgEventNotifier` | `notifier/postgres.go` | Real Postgres `NOTIFY <channel>, <payload>` via `pq.Listener` |
| Factory `New(...)` | `notifier/notifier.go:35-46` | Selects the right impl based on `cfg.Database.Type` |

The `EventNotifier` is already instantiated in `server/internal/app/server.go:198-211` and
is consumed by the check-worker fetcher/express loops
(`checkworker/worker.go:226,333`). `jobsvc.NewService(db)` simply doesn't take it as a
dependency.

Note: even on Postgres today, `NOTIFY jobs` is fire-and-forget — nothing listens on
that channel, so the emit has no functional effect on either backend. The listener side
is addressed in
[2026-05-17-09-jobsvc-getjobwait-listen-on-job-created.md](2026-05-17-09-jobsvc-getjobwait-listen-on-job-created.md).

## Goal

`jobsvc` must never issue dialect-specific raw SQL. Route the wake-up emit through
`EventNotifier` so:

- SQLite uses `LocalEventNotifier` (channels) — WARN disappears.
- Postgres uses `PgEventNotifier` — emits `NOTIFY job_created, '{}'` as before but via
  the abstraction.

No behavior changes beyond silencing the log noise. The listener wiring and polling
reduction are deferred to Spec 09.

## Non-goals

- Adding a `LISTEN` consumer in `GetJobWait` (Spec 09).
- Adding `EventTypeJobCreated` to `models.EventType` — job wake-ups are not audit events.
- Touching any other dialect-branched code in `jobsvc` (`CancelPendingForIncident`,
  `claimNextJob`).

## Implementation

### 1. Add a local channel-name constant (`service.go`)

```go
const (
    maxRetryCount = 2

    // eventTypeJobCreated is the notifier event type emitted when a new job is
    // created and ready to run soon. PgEventNotifier converts "." to "_" so the
    // on-the-wire Postgres channel name becomes "job_created".
    eventTypeJobCreated = "job.created"
)
```

### 2. Inject `notifier.EventNotifier` into `serviceImpl` (`service.go`)

```go
type serviceImpl struct {
    db       *bun.DB
    notifier notifier.EventNotifier
}

func NewService(db *bun.DB, n notifier.EventNotifier) Service {
    return &serviceImpl{db: db, notifier: n}
}
```

Remove the `"github.com/uptrace/bun/dialect/pgdialect"` import if it is now unused
(it is still used by `CancelPendingForIncident` and `claimNextJob` — keep it).

### 3. Replace the raw `NOTIFY` in `createNewJob` (`service.go:218-223`)

```go
// Wake up waiting job runners when the job is due within 15 minutes.
if time.Until(job.ScheduledAt) <= 15*time.Minute {
    _ = s.notifier.Notify(ctx, eventTypeJobCreated, "{}")
}
```

Payload `"{}"` follows the "wake up and re-poll" convention established by
`checkworker/worker.go:354-363` (where an empty `check_uid` is silently tolerated).

### 4. Update the four call sites for `NewService`

**`server/internal/app/server.go:192`** — the `eventNotifier` is already constructed a
few lines below (line 198-211); move it above `jobService` and pass it in:

```go
eventNotifier, err := notifier.New(dbService.DB(), cfg.Database.Type, connString, slog.Default())
if err != nil {
    return nil, fmt.Errorf("create event notifier: %w", err)
}

jobService := jobsvc.NewService(dbService.DB(), eventNotifier)
```

**`server/internal/handlers/escalationpolicies/runtime_test.go:32`**,
**`server/internal/handlers/incidents/resolve_test.go:39`**,
**`server/internal/handlers/incidents/validating_test.go:44`** — tests use SQLite;
pass `notifier.NewLocalEventNotifier()` (zero external deps):

```go
jobs := jobsvc.NewService(dbSvc.DB(), notifier.NewLocalEventNotifier())
```

## Files to change

### Modified files

- `server/internal/jobs/jobsvc/service.go` — add constant, inject notifier, replace NOTIFY
- `server/internal/app/server.go` — reorder notifier construction, pass to `NewService`
- `server/internal/handlers/escalationpolicies/runtime_test.go` — update `NewService` call
- `server/internal/handlers/incidents/resolve_test.go` — update `NewService` call
- `server/internal/handlers/incidents/validating_test.go` — update `NewService` call

## Verification

```bash
make build
make lint
make test
```

Manual smoke test — SQLite (no WARN):

```bash
make dev-test
# In another terminal, create a check that triggers a notification job, or
# POST directly to the jobs debug endpoint. Watch server log:
# Confirm: no "SQL query failed" / operation=NOTIFY entries.
```

Manual smoke test — Postgres (still emits to the right channel):

```bash
docker-compose up -d
make dev
# In a psql session:
psql "$DATABASE_URL" -c "LISTEN job_created;"
# Trigger a job creation. Confirm: "Asynchronous notification 'job_created' received."
```

## Risk log

| Risk | Mitigation |
|---|---|
| Missed call site → CI break | `rtk grep -rn "jobsvc.NewService" --include="*.go"` lists all 4 sites |
| Postgres channel name changes from `jobs` to `job_created` | No consumer exists today; the rename is safe |
| Future contributor re-introduces raw NOTIFY | Leave a `// dialect-agnostic — use s.notifier.Notify, never raw SQL` comment above `createNewJob` |
