# Reduce per-result DB round-trips in the check worker hot path

## Context

A 5-minute CPU profile captured on 2026-06-05 (pprof via `SP_PROFILER_ENABLED=true`, `localhost:6060/debug/pprof/profile?seconds=300`) showed the check-result write path as the dominant cost:

| Call chain | Cumulative |
| --- | --- |
| `checkworker.runnerLoop → executeJob → saveResult → SaveResultWithStatusTracking` | 72% |
| → `bun UpdateQuery → sqlite exec → syscall.rawsyscalln` | 69% |
| → `_readDbPage → pread` (page-cache misses on write) | 63% |
| `checkworker.processIncidents` | 8% |
| `runtime.kevent` (Go scheduler) | 4.5% |

Total CPU: **1.99 s over 300 s = 0.66%** — the process is I/O-bound, not CPU-bound. The dominant cost is SQLite page-cache misses on writes; that cannot be avoided. However, the incident-processing path wastes **3–4 extra round-trips per check result** on top of the unavoidable result INSERT.

**Current round-trips per result** (hot path):
1. `GetCheck` ([worker.go:682](server/internal/checkworker/worker.go:682)) — fetch the check to drive the incident state machine
2. `IsCheckInActiveMaintenance` ([incidents/service.go:89](server/internal/handlers/incidents/service.go:89)) — unconditional, almost always returns false
3. `FindActiveIncidentByCheckUID` ([incidents/service.go:117](server/internal/handlers/incidents/service.go:117))
4. `UpdateCheckStatus` ([incidents/service.go:130](server/internal/handlers/incidents/service.go:130))
5. `UpdateCheckIncidentClocks` ([incidents/service.go:133](server/internal/handlers/incidents/service.go:133))
6. result-save transaction (UPDATE `last_for_status` + INSERT result)

**Target after this spec:**
1. ~~`GetCheck`~~ — amortized into claim time (item 2)
2. ~~`IsCheckInActiveMaintenance`~~ — cached with 60 s TTL (item 3)
3. `FindActiveIncidentByCheckUID`
4. `UpdateCheckStatusAndClocks` — one UPDATE replacing two (item 1)
5. result-save transaction (unchanged — already optimally indexed)

This spec is the follow-up optimization work explicitly deferred by [specs/done/2026/05/2026-05-16-01-perf-instrumentation-and-loadgen.md](specs/done/2026/05/2026-05-16-01-perf-instrumentation-and-loadgen.md), which added the stage-timing metrics and `make bench-checks` baseline harness.

Scope: only the three items below. The result-save transaction itself is untouched (the `idx_results_last_for_status` partial index is already optimal).

---

## Item 1 — Merge `UpdateCheckStatus` + `UpdateCheckIncidentClocks` into one UPDATE

### Problem

`ProcessCheckResult` ([incidents/service.go:130–138](server/internal/handlers/incidents/service.go:130)) always calls these two methods sequentially on the same `checks` row. They are currently two separate `UPDATE` statements — two round-trips, and non-atomic (a crash between them would leave status updated but clocks stale).

The only production call sites for both methods are lines 130 and 133 of `incidents/service.go`. There is no site that calls only one of them.

### Change

**New model type** — promote the private `incidentClocks` struct ([incidents/service.go:~155](server/internal/handlers/incidents/service.go:155)) to a shared type in `internal/db/models/`:

```go
// internal/db/models/incident_clock_update.go
type IncidentClockUpdate struct {
    FirstFailureAt                  *time.Time
    ClearFirstFailureAt             bool
    FirstSuccessSinceFailureAt      *time.Time
    ClearFirstSuccessSinceFailureAt bool
}
```

**New `db.Service` method** — replace the two old methods with one:

```go
UpdateCheckStatusAndClocks(
    ctx context.Context,
    checkUID string,
    status models.CheckStatus,
    streak int,
    statusChangedAt *time.Time,
    clocks models.IncidentClockUpdate,
) error
```

Single `UPDATE checks SET status=?, status_streak=?, updated_at=?, [status_changed_at=?,] [first_failure_at=?,] [first_success_since_failure_at=?] WHERE uid=? AND deleted_at IS NULL`. Tri-state clock semantics (nil+!clear = leave column untouched; nil+clear = NULL; non-nil = set value) are preserved. `updated_at` is written once.

**Delete** `UpdateCheckStatus` and `UpdateCheckIncidentClocks` from:
- `internal/db/service.go` (interface)
- `internal/db/sqlite/sqlite.go:2312` and `:2336`
- `internal/db/postgres/postgres.go:2321` and `:2344`
- Mock implementations in `internal/notifications/slack_test.go` and `internal/handlers/incidents/validating_test.go`

**Update call site** in `incidents/service.go:130–138` to call `UpdateCheckStatusAndClocks`.

Also replace the private `incidentClocks` struct throughout `incidents/service.go` with `models.IncidentClockUpdate`. `deriveIncidentClocks` and `applyClocks` return/accept the new type.

**Bonus**: status and clocks are now written atomically — a mid-write crash can no longer leave them inconsistent.

---

## Item 2 — Eliminate per-result `GetCheck` by fetching at claim time

### Problem

`processIncidents` ([worker.go:682](server/internal/checkworker/worker.go:682)) calls `GetCheck` on every single result to supply the incident state machine with check configuration and current status/streak/clock fields. At N checks/min this is N extra full-row `SELECT *` queries per minute that could be amortized.

### Correctness analysis

The check fields read by `ProcessCheckResult` ([incidents/service.go:83](server/internal/handlers/incidents/service.go:83)) fall into two groups:

- **State-machine fields** (`Status`, `StatusStreak`, `FirstFailureAt`, `FirstSuccessSinceFailureAt`): written **exclusively** by `ProcessCheckResult` itself via item 1's merged UPDATE. Each check_job has a unique check_uid; the lease serializes execution — only one worker processes a given check at a time. Therefore the claim-time snapshot of these fields is the correct prior-state for the very next execution.
- **Config fields** (`ConfirmationPeriodSeconds`, `RecoveryPeriodSeconds`, `EscalationThreshold`, `ReopenCooldownMultiplier`, `Period`, `CheckGroupUID`, `EscalationPolicyUID`, `Slug`, `Name`, `OrganizationUID`, `UpdatedAt`): set by the user via the checks API or `UpdateCheck`. These are stable between claim and execution but could be up to `FetchMaxAhead` (default 5 min, [config.go:391](server/internal/config/config.go:391)) + one check period stale.

**Accepted staleness**: a user editing check thresholds or period mid-execution sees the old value for at most ~6 min. The `UpdatedAt` reopen-guard ([incidents/service.go:~447](server/internal/handlers/incidents/service.go)) may use a claim-time snapshot; this is acceptable — the guard prevents re-opening an incident for a check that was just reconfigured, and a ≤6 min window is a reasonable grace period.

### Change

**Transient field on `CheckJob`**:

```go
// internal/db/models/check_job.go
type CheckJob struct {
    // ... existing fields ...

    // Check is populated at claim time and not persisted.
    Check *Check `bun:"-"`
}
```

**Batch fetch in `ClaimJobs`** ([checkjobsvc/service.go:63](server/internal/checkworker/checkjobsvc/service.go:63)):

After `updateJobsWithLease` completes (all jobs claimed), and while still inside the transaction, do one batched select:

```go
checkUIDs := make([]string, len(jobs))
for i, j := range jobs { checkUIDs[i] = j.CheckUID }
var checks []*models.Check
_ = tx.NewSelect().Model(&checks).
    Where("uid IN (?)", bun.In(checkUIDs)).
    Scan(ctx)
// stitch: build map[uid]*Check, assign to job.Check
```

This avoids a JOIN (which would require `FOR UPDATE OF` on Postgres with `SKIP LOCKED` — complex and error-prone). A separate SELECT inside the same tx is sufficient and simple.

Apply the same pattern to `ClaimJobsForCheck` ([checkjobsvc/service.go:112](server/internal/checkworker/checkjobsvc/service.go:112)).

**Worker uses attached check**:

```go
// worker.go processIncidents
func (r *CheckWorker) processIncidents(ctx context.Context, checkJob *models.CheckJob, result *models.Result) {
    check := checkJob.Check
    if check == nil {
        // fallback: check was deleted or not fetched
        var err error
        check, err = r.dbService.GetCheck(ctx, checkJob.OrganizationUID, checkJob.CheckUID)
        if err != nil { ... return }
    }
    if err := r.incidentSvc.ProcessCheckResult(ctx, check, result); err != nil { ... }
}
```

The `ProcessCheckResult` signature is unchanged — passive handlers (heartbeat, workers, emailcheck) fetch the check themselves and pass it directly; they are unaffected.

---

## Item 3 — Maintenance-window TTL cache

### Problem

`IsCheckInActiveMaintenance` ([incidents/service.go:89](server/internal/handlers/incidents/service.go:89)) is called unconditionally on every result and is almost always false. It issues a DB query (one SELECT of candidate windows + Go-side recurrence evaluation via `models.IsActiveAt`), with the sole caller being `ProcessCheckResult`. Maintenance windows are created/updated/deleted infrequently and currently emit **no EventNotifier event**.

### Change

**New `db.Service` method** (replaces the bool method):

```go
ListMaintenanceWindowsForCheck(ctx context.Context, checkUID string) ([]*models.MaintenanceWindow, error)
```

Extracts the existing SELECT from `IsCheckInActiveMaintenance` in both backends ([sqlite.go:3886](server/internal/db/sqlite/sqlite.go:3886), [postgres.go:3892](server/internal/db/postgres/postgres.go:3892)). Returns the raw window rows; does not evaluate recurrence. Delete `IsCheckInActiveMaintenance` from the interface and both implementations.

**Strong-reference TTL cache in `incidents.Service`**:

```go
type mwCacheEntry struct {
    windows   []*models.MaintenanceWindow
    fetchedAt time.Time
}

type Service struct {
    // ... existing fields ...
    mwCache   map[string]mwCacheEntry  // keyed by checkUID
    mwCacheMu sync.RWMutex
}

const maintenanceWindowCacheTTL = 60 * time.Second
```

Use the already-injected `clock.Clock` for time comparisons (testability). On cache hit within TTL, evaluate `models.IsActiveAt(window, s.clock.Now())` in-process against the cached window slice. On miss, call `ListMaintenanceWindowsForCheck`, populate cache, evaluate.

Do **not** use `internal/utils/cache/cache.go` — it stores `weak.Pointer` values, meaning entries are GC'd almost immediately when no other strong reference holds the slice. A plain `sync.RWMutex` + `map` is correct and sufficient.

**Staleness tradeoff**: window *definitions* (start/end/recurrence rules) can be up to 60 s stale after create/edit/delete/re-association. Active/inactive *transitions* (e.g., a window becoming active at a scheduled time) stay exact because `IsActiveAt(now)` is evaluated against the cached definitions on every result — the clock advances even when definitions are cached.

**Possible follow-up** (not in this spec): emit a `maintenance_window.changed` event from `maintenancewindows/service.go` mutations and listen in `incidents.Service` to invalidate early. The EventNotifier pattern exists ([notifier/notifier.go](server/internal/notifier/notifier.go)); wiring it up is straightforward but out of scope here.

---

## Critical files

| File | Change |
| --- | --- |
| `internal/db/models/incident_clock_update.go` | **NEW**: `IncidentClockUpdate` type |
| `internal/db/models/check_job.go` | Add transient `Check *Check` field with `bun:"-"` |
| `internal/db/service.go` | Replace `UpdateCheckStatus` + `UpdateCheckIncidentClocks` with `UpdateCheckStatusAndClocks`; replace `IsCheckInActiveMaintenance` with `ListMaintenanceWindowsForCheck` |
| `internal/db/sqlite/sqlite.go:2312,2336,3886` | Implement `UpdateCheckStatusAndClocks`, `ListMaintenanceWindowsForCheck`; delete old methods |
| `internal/db/postgres/postgres.go:2321,2344,3892` | Same |
| `internal/handlers/incidents/service.go:83,130,133,155` | Use `UpdateCheckStatusAndClocks`; TTL cache for maintenance; replace private `incidentClocks` with `models.IncidentClockUpdate` |
| `internal/checkworker/checkjobsvc/service.go:63,112` | Batch check fetch inside `ClaimJobs` / `ClaimJobsForCheck` tx |
| `internal/checkworker/worker.go:682` | `processIncidents` uses `checkJob.Check`, falls back to `GetCheck` |
| `internal/notifications/slack_test.go` | Update mock |
| `internal/handlers/incidents/validating_test.go` | Update mock + add `UpdateCheckStatusAndClocks` test cases |

---

## Out of scope

- Result-save transaction tuning (already optimal with `idx_results_last_for_status`).
- Slim check projection (select only the ~17 fields needed by `ProcessCheckResult` instead of `SELECT *`). Low impact, high churn — defer.
- Event-based maintenance cache invalidation (no maintenance events today). Named as a follow-up above.
- SQLite `PRAGMA cache_size` / WAL tuning — separate concern.

---

## Acceptance criteria

1. **Item 1**: `UpdateCheckStatus` and `UpdateCheckIncidentClocks` no longer exist. A single `UPDATE` on `checks` is issued per result, containing both status and clock columns. Tri-state semantics verified: (a) nil + !clear → column unchanged, (b) nil + clear → NULL written, (c) non-nil → value written. Tests cover all three branches for both backends.
2. **Item 2**: After `ClaimJobs`, each returned `CheckJob.Check` is non-nil (test with in-memory DB or mock). `GetCheck` is not called during `processIncidents` for a normally-claimed job (mock call-count assertion). Fallback path (nil Check) calls `GetCheck` once and continues correctly.
3. **Item 3**: `IsCheckInActiveMaintenance` no longer exists. In a test with an injected mock clock advancing past TTL, `ListMaintenanceWindowsForCheck` is called once per TTL per check-uid regardless of how many results arrive. `models.IsActiveAt` is still evaluated on every result (active/inactive transitions are not delayed by TTL) — verify by advancing only the clock, not re-fetching, and confirming a window that becomes active mid-TTL is correctly detected.
4. All passive-handler paths (heartbeat, workers/submit-result, emailcheck) continue to pass existing tests unchanged.
5. `make build-backend lint-back test` passes with no new lint findings.

---

## Verification

1. `make build-backend lint-back test` — clean.
2. `make bench-checks` before and after: compare `solidping_check_stage_duration_seconds{stage="process_incident"}` p95 — expect a measurable reduction on both backends under load.
3. Under `make dev` with `SP_PROFILER_ENABLED=true`, capture a 60 s CPU profile (`curl "http://localhost:6060/debug/pprof/profile?seconds=60" -o /tmp/after.pprof`) and verify `GetCheck` no longer appears in the incident processing call tree.
4. Create a maintenance window for a check via the API; confirm the window is active within ≤60 s (one TTL cycle) without needing a server restart.

---

## Implementation Plan

1. **Item 1: merge the two UPDATEs** — lowest risk, pure internal refactor.
   - Create `internal/db/models/incident_clock_update.go`.
   - Add `UpdateCheckStatusAndClocks` to `db.Service` interface and both impls.
   - Update `incidents/service.go`: replace private `incidentClocks` with `models.IncidentClockUpdate` in `deriveIncidentClocks`, `applyClocks`, and the `ProcessCheckResult` call site.
   - Delete `UpdateCheckStatus` and `UpdateCheckIncidentClocks` everywhere.
   - Update mocks; add tri-state test cases to `validating_test.go`.
   - Commit.

2. **Item 3: maintenance-window TTL cache** — self-contained, no worker changes.
   - Add `ListMaintenanceWindowsForCheck` to `db.Service` and both impls (extract the SELECT from `IsCheckInActiveMaintenance`).
   - Add `mwCache map[string]mwCacheEntry` + `sync.RWMutex` to `incidents.Service`; write the cache lookup/store logic using `clock.Clock`.
   - Delete `IsCheckInActiveMaintenance` from the interface and both impls.
   - Update mocks; add TTL cache unit tests with a fake clock.
   - Commit.

3. **Item 2: batch check fetch at claim time** — touches the claim path.
   - Add transient `Check *Check` field to `models.CheckJob`.
   - Extend `ClaimJobs` and `ClaimJobsForCheck` in `checkjobsvc/service.go` to batch-fetch checks inside the tx and stitch.
   - Update `worker.go processIncidents` to use `checkJob.Check` with `GetCheck` fallback.
   - Add mock call-count test asserting `GetCheck` is not called for a normally-claimed job.
   - Commit.

4. **QA**: run `make bench-checks`; compare `process_incident` stage metric against pre-optimization baseline under `bench-results/`. Capture pprof profile.

5. **Audit + archive**: move this spec to `specs/done/YYYY/MM/` once all acceptance criteria pass.
