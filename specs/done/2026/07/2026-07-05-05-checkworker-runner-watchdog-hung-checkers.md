# Check-worker runner watchdog — a hung or panicking checker must never take down the fleet

## Context

Incident 2026-07-04/05 on the k8xp dev cluster: mail checkers with unbounded
socket reads (spec `2026-07-05-04`) hung forever on
`outlook.office365.com:993/:995`. Each lease claim permanently consumed one
runner goroutine; within ~45 minutes **all 25 runners of the eu2 worker were
stuck** (`freeRunners=0`, every runner's last log line an `Executing check
job` for an outlook check) and the region stopped executing *all* checks.
`us-1` and the in-server `default` worker were degrading the same way. The
affected check showed status `created` forever; its job rows showed
`leaseStarts` climbing past the `crashLoopThreshold = 9`
(`server/internal/handlers/checkjobs/service.go:24`) into a state the
dashboard calls `crashLooping` — a misnomer, since nothing crashed or looped;
the executions never ended.

Spec `2026-07-05-04` fixes the three offending checkers. This spec is the
**airbag**: with ~40 checker types, many wrapping third-party clients, the
worker must assume some future `Execute` will ignore its context (or panic)
and must contain the blast radius to one bounded goroutine instead of a
runner — because a lost runner is silent, cumulative, and fleet-fatal.

## Current state (verified 2026-07-05; re-verify at build)

- `executeJob` calls `result, err := checker.Execute(execCtx, checkConfig)`
  **synchronously** (`server/internal/checkworker/worker.go:692`), where
  `execCtx` carries the cost-aware timeout
  (`r.schedParams.ExecutionTimeout(...)`, clamped ≤ 30s, `worker.go:687`).
  If `Execute` never returns, the runner goroutine (`runnerLoop`,
  `worker.go:518`, `poolSize` started at `worker.go:228`) is gone for the
  process lifetime; no result, no lease release, no log, no metric.
- There is **no `recover()` anywhere in `checkworker`** — a panic inside any
  checker kills the entire worker process (on the main server pod that also
  means the API).

## Design decisions

### D1 — Run `Execute` in a child goroutine; abandon it past deadline + grace

Wrap the call:

```go
type execOutcome struct { result *checkerdef.Result; err error }
ch := make(chan execOutcome, 1)            // cap 1: late sender never blocks
go func() {
    defer func() {                          // D2: panic containment
        if p := recover(); p != nil {
            ch <- execOutcome{err: fmt.Errorf("%w: %v", ErrCheckerPanic, p)}
        }
    }()
    res, err := checker.Execute(execCtx, checkConfig)
    ch <- execOutcome{res, err}
}()

select {
case out := <-ch:
    // existing paths: result / DeadlineExceeded / error
case <-time.After(execTimeout + abandonGrace):
    // checker ignored its context — abandon it
}
```

`abandonGrace` (constant, 5s) gives well-behaved checkers room to observe
`execCtx.Done()` and return their own `StatusTimeout` first — the watchdog
only fires for checkers that ignore the context. Normal path cost: one
goroutine + one buffered channel per execution (negligible next to network
I/O; the express path reuses the same wrapper).

### D2 — Panics become error results, not process crashes

The child's `recover()` converts a checker panic into
`ErrCheckerPanic` delivered over the channel; `executeJob` records a
`StatusError` result with the panic message via the existing error branch
(`worker.go:694-717`) and logs at ERROR with a stack
(`debug.Stack()` captured inside the deferred recover). The runner survives.

### D3 — Abandonment is loud and measured

On the abandon branch:

- Log ERROR `"Checker abandoned: did not honor context"` with `check_uid`,
  `check_type`, `org`, `region`, and the abandon count.
- Metrics (in `prommetrics`):
  `solidping_check_runner_abandoned_total{check_type}` counter and
  `solidping_check_runner_abandoned_active` gauge — incremented on abandon,
  decremented by the child's deferred function if it ever finishes.
  The gauge is the direct "leaked goroutines right now" signal that was
  invisible during the incident.
- Save a `StatusTimeout` result with output
  `{"error": "check execution abandoned: checker did not honor its context"}`
  through the existing save path, then release the lease normally — the
  check surfaces as `timeout` on the dashboard instead of eternal `created`,
  and `leaseStarts` stops climbing.

### D4 — A late result from an abandoned checker is discarded

The watchdog already saved a timeout result and released the lease; the
child's eventual channel send lands in the cap-1 buffer that nobody reads
and is garbage-collected with the goroutine. No double results, no
double lease release. (The child holds no locks and writes nothing itself —
all persistence stays on the runner side.)

## Implementation

1. Extract the execute-call block of `executeJob` (`worker.go:680-717`) into
   `runCheckerGuarded(ctx, checker, cfg, execTimeout) (result, err)`
   implementing D1/D2/D4.
2. Add the two metrics to `server/internal/prommetrics` and the abandon
   branch per D3.
3. Apply the same wrapper to the express-runner execution path if it calls
   `checker.Execute` directly (verify at build — `expressLoop`,
   `worker.go:239`).
4. Tests (`checkworker/worker_test.go`, stub checkers via the registry
   pattern already used there):
   - Hanging stub (blocks on a never-closed channel, ignores ctx): runner
     returns within `execTimeout + grace + ε`, a `timeout` result row exists,
     counter and gauge incremented, runner processes a subsequent job.
   - Late finish: hanging stub unblocks after abandonment → no second result
     row, gauge decrements.
   - Panicking stub: `StatusError` result with panic text, process alive,
     runner processes a subsequent job.
   - Well-behaved ctx-honoring slow checker (`checksleep`): watchdog does
     **not** fire; existing timeout semantics unchanged.

## Out of scope

- Fixing the mail checkers themselves (spec `2026-07-05-04`).
- Renaming the `crashLooping` derived state / adding a distinct "hung" state
  to the check-jobs admin view — worth a UX pass, but the state becomes
  near-unreachable once results flow again.
- Force-killing abandoned goroutines (impossible in Go); the gauge plus
  per-checker deadlines keep the leak observable and bounded.

## Verification

- `make test` (new worker tests) + `make lint`.
- Manual soak: register a deliberately-hanging check type behind
  `SP_RUNMODE=test`, let it cycle 10+ leases → `freeRunners` stays at
  poolSize−1 minimum, gauge reports the abandoned count, check shows
  `timeout` results.

## Key files

- `server/internal/checkworker/worker.go` (`executeJob` ~680–717, `runnerLoop` 518, `expressLoop` 239)
- `server/internal/prommetrics/` (new counter + gauge)
- `server/internal/checkworker/worker_test.go`

## Risk log

- **Grace too short** would double-report timeouts (watchdog result + late
  checker result) — mitigated by D4's discard semantics; the watchdog result
  always wins.
- **Goroutine-per-execution overhead** is noise relative to check I/O; the
  buffered channel guarantees no goroutine leak on the happy path.
- **recover() masking real bugs**: panics are logged at ERROR with stack and
  counted; they were previously fatal to the whole process, which is worse.

## Implementation Plan

1. **`ErrCheckerPanic` + `runCheckerGuarded`** (`server/internal/checkworker/worker.go`):
   - Add `ErrCheckerPanic = errors.New("checker panicked")` to the existing
     error var block.
   - Add `const abandonGrace = 5 * time.Second` next to the other worker
     constants.
   - Extract lines ~692 (`result, err := checker.Execute(execCtx, checkConfig)`)
     into a new method
     `func (r *CheckWorker) runCheckerGuarded(ctx context.Context, logger *slog.Logger, checker checkerdef.Checker, checkConfig checkerdef.Config, checkJob *models.CheckJob, execTimeout time.Duration) (*checkerdef.Result, error)`.
     Internally: spawn the child goroutine with `recover()` → `ErrCheckerPanic`
     wrapping `debug.Stack()` into the error text (D2), cap-1 buffered channel
     (D4), `select` between the channel and
     `time.After(execTimeout + abandonGrace)`.
   - On the abandon branch (D3): increment
     `prommetrics.CheckRunnerAbandoned` (counter) and
     `prommetrics.CheckRunnerAbandonedActive` (gauge, +1), log ERROR
     `"Checker abandoned: did not honor context"` with check_uid/check_type/
     org/region/abandon-count, and return a sentinel `(*checkerdef.Result,
     nil)` already shaped as the D3 `StatusTimeout` result (output
     `{"error": "check execution abandoned: checker did not honor its
     context"}`) so `executeJob` can save it through the existing path
     without new branching. The child goroutine's deferred cleanup
     decrements the gauge when (if) it eventually completes — this must run
     unconditionally (both normal-finish and panic-recover paths), so it
     lives in one `defer` that always fires, guarded by a `sync.Once`-free
     flag captured at abandon time (only decrement if the abandon branch
     actually incremented).
   - Track "did the select observe an abandon" via a local bool so the
     child's own deferred decrement only fires when a matching increment
     happened (D3's "decremented by the child's deferred function if it ever
     finishes" — the increment marks the gauge as "currently leaked",
     decrement clears it once the goroutine is no longer outstanding).
   - `executeJob` calls `runCheckerGuarded` instead of `checker.Execute`
     directly, keeping the existing `errors.Is(err, context.DeadlineExceeded)`
     / generic-error branches for the non-abandoned paths unchanged (D1: the
     watchdog is additive, not a replacement of existing timeout semantics).
2. **Metrics** (`server/internal/prommetrics/metrics.go` + `recording.go`):
   - `CheckRunnerAbandoned` — `CounterVec` `solidping_check_runner_abandoned_total`,
     label `check_type`.
   - `CheckRunnerAbandonedActive` — `Gauge`
     `solidping_check_runner_abandoned_active` (no labels — a single
     fleet-wide "currently leaked goroutines" signal per D3's own wording;
     per-worker breakdown isn't asked for and would need a worker_uid label
     the spec doesn't mention).
   - Add both to `allCollectors`.
   - `RecordCheckRunnerAbandoned(checkType string)` and
     `IncCheckRunnerAbandonedActive()` / `DecCheckRunnerAbandonedActive()`
     helpers in `recording.go`, following the existing Record*/Set* naming.
3. **`expressLoop` / `handleExpressEvent` path**: `handleExpressEvent` calls
   `r.executeJob` (worker.go:509), and `executeJob` is the single call site
   being changed in step 1 — so the express path is covered for free once
   `executeJob` routes through `runCheckerGuarded`. No separate wrapper
   needed at `expressLoop` (worker.go:239) itself; verified at read-time that
   `expressLoop` never calls `checker.Execute` directly, only through
   `handleExpressEvent` → `executeJob`.
4. **Tests** (`server/internal/checkworker/worker_test.go`), two layers:
   - **Unit layer**: call `runCheckerGuarded` directly with hand-built stub
     `checkerdef.Checker` implementations defined in the test file (no
     registry changes — the registry is a hardcoded switch and the spec's
     `runCheckerGuarded(ctx, checker, cfg, execTimeout)` signature already
     takes a `checkerdef.Checker` value, so stubs are passed straight in):
     - `hangingChecker` — `Execute` blocks on a never-closed channel,
       ignores ctx entirely.
     - `panickingChecker` — `Execute` panics immediately.
     - `lateFinishChecker` — like `hangingChecker` but the test unblocks it
       after the watchdog fires, to prove D4 (late send is discarded, no
       double result).
     - Well-behaved case reuses the existing `checksleep.SleepChecker` (real
       registered type) via `registry.GetChecker(checkerdef.CheckTypeSleep)`
       — it already honors ctx (proven by
       `TestSleepChecker_Execute_ContextTimeout`), so the watchdog must not
       fire for it.
     - Use a short `execTimeout` (e.g. 50ms) so `execTimeout + abandonGrace`
       in tests isn't the real 5s constant — inject a test-only grace via an
       unexported package-level `var abandonGraceOverride time.Duration` read
       by `runCheckerGuarded` when non-zero (test sets it small, e.g. 100ms,
       and resets via `t.Cleanup`), so hang/panic tests finish in well under
       a second instead of waiting a real 5s grace.
   - **Integration layer** (proves the actual point of the spec — the runner
     survives to process a subsequent job): drive the real pool machinery
     (`runnerLoop` + `jobsChan` + `availableRunners`) with a small in-test
     harness that swaps in a stub checker for one job and a normal
     heartbeat/sleep job for the next, asserting:
     - hang case: a `timeout` result row is saved for job 1, the abandon
       counter and gauge both moved, and job 2 (sent right after) completes
       normally within the test timeout — proving the runner goroutine is
       still alive and pulling from `jobsChan`.
     - late-finish case: after job 1's watchdog fires, unblock the hung
       goroutine and confirm no second result row appears and the gauge
       returns to its pre-test value.
     - panic case: job 1's checker panics, an `error` result row with the
       panic text is saved, the test process is still running (implicit —
       if recover() didn't work the whole `go test` binary would crash), and
       job 2 completes normally.
     - Since `executeJob` resolves checkers via `registry.GetChecker`
       keyed on `checkJob.Type`, and the registry can't be swapped in tests,
       the integration layer calls `runCheckerGuarded` directly from a
       small helper that otherwise mirrors `executeJob`'s save-result +
       release-lease sequence (reusing `r.saveResult` / `r.releaseLease`),
       driven through a real `runnerLoop` goroutine consuming from
       `r.jobsChan` — this keeps the "runner survives" proof anchored in the
       actual pool code path (`runnerLoop`'s `for` loop / `availableRunners`
       bookkeeping) rather than a bare function call, without requiring a
       registry seam that's out of scope for this spec.
5. Run `make fmt`, then `make build-backend lint-back test`, fixing anything
   that fails, committing granularly per the workflow's QA step.
