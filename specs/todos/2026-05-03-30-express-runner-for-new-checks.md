# Express runner for newly-created checks

## Context

When a user creates a check via `POST /api/v1/orgs/$org/checks`, the backend already does most of the right things synchronously: the `check_jobs` row is inserted in the same transaction with `scheduled_at = now` (`server/internal/db/postgres/postgres.go:944`), an initial "Created" placeholder result is written, and a `check.created` notification is published via `EventNotifier.Notify` (`server/internal/handlers/checks/service.go:1236`). The check worker's `fetcherLoop` (`server/internal/checkworker/worker.go:206`) is woken by that event, calls `ClaimJobs`, and dispatches to a runner.

The remaining delay shows up when the regular runner pool is busy. `CheckWorker.Nb` defaults to 3. If all three runners are mid-execution on slow checks (HTTP timeouts, browser checks, large DNS lookups), the new job sits idle until one of them finishes and signals `completionChan` (`server/internal/checkworker/worker.go:243`). For a user clicking "Create" and watching, this manifests as a few seconds of "Created" placeholder before the first real status lands.

We don't want to fix this by inflating the regular pool — a higher `Nb` helps but doesn't *guarantee* low first-run latency. We want determinism: a freshly-created check should not have to share a queue with steady-state periodic work.

## Approach

Add a dedicated **express goroutine** to `CheckWorker` that:

1. Listens on its own `EventNotifier.Listen("check.created")` subscription.
2. Parses a `check_uid` from the event payload.
3. Atomically claims the `check_jobs` row(s) for that check (region-filtered).
4. Calls the existing `executeJob` (`server/internal/checkworker/worker.go:348`) to run the check, save the result, and release/reschedule the lease.

The express goroutine processes one job at a time, in serial. New checks are rare events compared to periodic re-runs; one extra goroutine is enough. If two checks are created in quick succession, the second waits behind the first inside the express path, but the regular fetcher still sees both via the normal `scheduled_at <= now` query and will pick the second one up if the express goroutine is busy. Worst case degrades to today's behaviour.

There is no double-execution risk: lease claim is atomic via `SELECT FOR UPDATE SKIP LOCKED` on Postgres / optimistic locking on SQLite (`server/internal/checkworker/checkjobsvc/service.go:88-132`). Whichever path wins claims the row; the other gets nothing.

## Scope

**In:**
- Include `check_uid` in the `check.created` event payload (currently `"{}"`).
- Add `ClaimJobsForCheck(ctx, workerUID, region, checkUID)` to `checkjobsvc.Service`, reusing `selectAvailableJobs` + `updateJobsWithLease` with one extra `WHERE check_uid = ?`.
- Add `expressLoop` to `CheckWorker`, registered in `Run` next to the existing `heartbeatLoop`/`runnerLoop`/`fetcherLoop` goroutines.
- Backend integration test: with all `Nb` runners blocked on a long-running check, a newly-created check still produces a real result row within an explicit timeout (~2 s).

**Out:**
- Bumping the default `CheckWorker.Nb` (still a sensible knob, but orthogonal — the express path makes this less urgent for first-run UX specifically).
- Adding `ORDER BY (lease_starts = 0) DESC` to the regular path (subsumed by the express path for first-run; nice-to-have for bulk-create scenarios but not in scope here).
- Frontend changes — see spec `2026-05-03-31-check-detail-first-run-polling.md`.

## Implementation

### 1. Event payload includes the check UID

`server/internal/handlers/checks/service.go:1236`:

```go
// before
if err := s.eventNotifier.Notify(ctx, string(eventType), "{}"); err != nil { … }

// after
payload, _ := json.Marshal(map[string]string{"check_uid": check.UID})
if err := s.eventNotifier.Notify(ctx, string(eventType), string(payload)); err != nil { … }
```

The fetcher's existing `case <-checkCreatedChan` consumer (`server/internal/checkworker/worker.go:245`) ignores the payload, so this is backward-compatible. `EventNotifier.Notify` passes the payload through verbatim on both backends (`server/internal/notifier/local.go:54`, `server/internal/notifier/postgres.go:131`).

### 2. Targeted claim method

Add to `server/internal/checkworker/checkjobsvc/service.go`, mirroring `ClaimJobs`:

```go
ClaimJobsForCheck(
    ctx context.Context,
    workerUID string,
    region *string,
    checkUID string,
) ([]*models.CheckJob, error)
```

Implementation reuses `selectAvailableJobs` and `updateJobsWithLease`. The select query gains one extra `Where("check_uid = ?", checkUID)`. Limit defaults to 4 (a check has at most one job per region; 4 is a generous cap). `maxAhead` is `0` (we only want jobs whose `scheduled_at <= now`, which is the common case for fresh checks). Region filter is identical to `ClaimJobs`: `region IS NULL OR ? LIKE region || '%'`.

Add a corresponding test in `server/internal/checkworker/checkjobsvc/service_test.go` covering the happy path, region mismatch (returns nothing), already-claimed (returns nothing), and lease-expired (re-claims).

### 3. Express loop in the worker

In `server/internal/checkworker/worker.go`, in `Run` (around line 130):

```go
r.wg.Add(1)
go r.expressLoop(ctx)
```

`expressLoop` body:

```go
func (r *CheckWorker) expressLoop(ctx context.Context) {
    defer r.wg.Done()

    logger := r.logger.With("role", "express")
    logger.InfoContext(ctx, "Express runner started")
    defer logger.InfoContext(ctx, "Express runner stopped")

    ch := r.services.EventNotifier.Listen("check.created")

    for {
        select {
        case <-ctx.Done():
            return
        case payload, ok := <-ch:
            if !ok {
                return
            }
            r.handleExpressEvent(ctx, logger, payload)
        }
    }
}

func (r *CheckWorker) handleExpressEvent(ctx context.Context, logger *slog.Logger, payload string) {
    var msg struct {
        CheckUID string `json:"check_uid"`
    }
    if err := json.Unmarshal([]byte(payload), &msg); err != nil || msg.CheckUID == "" {
        // Old senders publish "{}"; nothing for express to do.
        return
    }
    jobs, err := r.checkJobSvc.ClaimJobsForCheck(ctx, r.worker.UID, r.worker.Region, msg.CheckUID)
    if err != nil {
        logger.WarnContext(ctx, "express claim failed", "error", err, "check_uid", msg.CheckUID)
        return
    }
    for _, job := range jobs {
        if err := r.executeJob(ctx, logger, job); err != nil && !errors.Is(err, context.Canceled) {
            logger.ErrorContext(ctx, "express execution failed", "error", err, "check_uid", job.CheckUID)
        }
    }
}
```

`executeJob` already handles passive checks, result writing, lease release, and OTel spans — no changes there.

### 4. Test

Add to `server/internal/checkworker/worker_test.go` (table-driven, `t.Parallel()`, `testify/require`, follows existing conventions):

```
TestCheckWorker_ExpressRunsFreshCheckWhenPoolBusy
```

- Spin up `CheckWorker` with `Nb = 1`.
- Block the single regular runner on a fake check that sleeps for ~5 s.
- After ~50 ms, insert a second check + check_job and publish `check.created` with the second check's UID.
- Assert: a result row for the second check appears within 2 s, while the first runner is still blocked.

## Verification

End-to-end manual test:

1. `make dev-test`.
2. Create three HTTP checks against a slow target (e.g. `https://httpbin.org/delay/15`) so all three default runners are busy.
3. While those are still running, create a fourth check against `https://example.com`.
4. Confirm via worker logs that the fourth check executes via the express goroutine within ~1 s, not after waiting for a regular runner to free.
5. Confirm the OTel span and `executeJob` log line appear exactly once for the fourth check (no double execution).
6. `rtk go test ./server/...` and `rtk make lint` pass.

## Files touched

- `server/internal/handlers/checks/service.go` — `emitEvent` payload (line ~1236)
- `server/internal/checkworker/checkjobsvc/service.go` — add `ClaimJobsForCheck` + interface
- `server/internal/checkworker/checkjobsvc/service_test.go` — coverage for new method
- `server/internal/checkworker/worker.go` — add `expressLoop` + `handleExpressEvent`, register goroutine in `Run`
- `server/internal/checkworker/worker_test.go` — express-runs-when-pool-busy test
- `server/internal/notifications/slack_test.go` — mock `CheckJobs` service may need a `ClaimJobsForCheck` stub (interface is mocked there)

## Implementation Plan

1. Add `ClaimJobsForCheck` to the `Service` interface, implementation, and tests. Run those tests in isolation.
2. Update the `check.created` payload to JSON `{"check_uid":"…"}`. Confirm fetcher still wakes correctly (existing tests in `server/internal/notifier/*_test.go` should be untouched).
3. Add `expressLoop` + `handleExpressEvent` to the worker; register in `Run`.
4. Write the integration test that proves saturated-pool + new-check still yields a fast result.
5. `make gotest` + `make lint-back` clean.
6. Manual smoke per "Verification" above.
