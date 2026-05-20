# Clock abstraction

## Context

The incident pipeline makes about 30 direct calls to `time.Now()` in
[server/internal/handlers/incidents/service.go](server/internal/handlers/incidents/service.go)
alone, plus additional calls in the escalation-step and notification job runners
([server/internal/jobs/jobtypes/job_escalation_step.go](server/internal/jobs/jobtypes/job_escalation_step.go),
[server/internal/jobs/jobtypes/job_notification.go](server/internal/jobs/jobtypes/job_notification.go)).
All comparisons involving confirmation windows, recovery windows, reopen cooldowns, and
escalation repeat intervals read real wall-clock time.

The fast-loop integration tests introduced by
[2026-05-17-04-fast-loop-e2e-integration-tests.md](2026-05-17-04-fast-loop-e2e-integration-tests.md)
work around this by setting all threshold values to 0 or 1 (so the windows collapse to
nothing), but that only tests the degenerate case. A test that says "after 30 minutes
without acknowledgement, the escalation policy fires a repeat cycle" cannot be written
without either:

1. Actually waiting 30 minutes (unacceptable), or
2. A clock abstraction that lets a test advance time without sleeping.

This spec introduces a `Clock` interface, wires a real-time implementation in production,
and wires an injectable fake in tests. It is a **phased rollout** — Phase 1 covers the
incident/escalation/notification hot path; Phase 2 (future, out of scope here) covers the
check worker and job-scheduler tickers.

## Goal

- A `Clock` interface in a new `server/internal/utils/clock` package.
- `Real` implementation that delegates to `time.Now()`, `time.Since()`, etc.
- `Fake` implementation that starts at a fixed epoch and can be advanced with `Advance(d)`.
- Phase 1: replace `time.Now()` / `time.Since()` in `incidents/service.go` and the two
  job runner files listed above.
- The fast-loop test harness from spec 04 wires `Fake` so scenarios can advance time
  instead of sleeping.

### Honest opinion (recorded at planning time)

A clock abstraction touches many files and can introduce subtle bugs if a `time.Now()`
call is missed (a comparison uses real time, the condition is checked against fake time,
the result is wrong). The risk is manageable if the rollout is strictly phased: do the
incident state-machine first because it is both the highest-value target and the most
self-contained. Leave the check worker and job-scheduler tickers in real time for now —
they run as background goroutines with their own `time.Ticker` objects, which require a
more invasive refactor and yield less test value.

The alternative — injecting past timestamps directly into database rows to simulate
elapsed time — works today and is honest, but it requires each test to know the exact
columns to manipulate and it does not test the `time.Since` comparisons in the
state-machine code. A clock abstraction is the right long-term answer.

Library note: `jonboulle/clockwork` is a well-maintained Go fake-clock library that
provides exactly this interface. Using it avoids writing our own `Fake` from scratch. It
can be added as a dev-dependency (used only in test code and the clock package) or used
as the basis of our interface (wrap, don't embed — keep our interface narrow).

## Non-goals

- Phase 2: clock-ifying `checkworker` tickers, `jobsvc` polling intervals, snooze sweep,
  aggregation scheduler — out of scope for this spec.
- Exposing the clock as a HTTP-testable endpoint (no time-travel API).
- Supporting anything beyond `Now()`, `Since()`, `After()`, and `Sleep()` in Phase 1.

## Design

### The `Clock` interface

`server/internal/utils/clock/clock.go`

```go
package clock

import "time"

// Clock is a source of time. Real wraps the standard library; Fake allows
// tests to advance time deterministically.
type Clock interface {
    Now() time.Time
    Since(t time.Time) time.Duration
    After(d time.Duration) <-chan time.Time
    Sleep(d time.Duration)
}
```

**`Real`** (`clock_real.go`):

```go
type Real struct{}

func (Real) Now() time.Time                        { return time.Now() }
func (Real) Since(t time.Time) time.Duration       { return time.Since(t) }
func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (Real) Sleep(d time.Duration)                  { time.Sleep(d) }
```

**`Fake`** (`clock_fake.go`): wraps `jonboulle/clockwork.FakeClock` or is hand-rolled.
Exposes:

```go
type Fake struct { ... }

func NewFake(start time.Time) *Fake
func (f *Fake) Advance(d time.Duration)  // advance clock; notify all waiting Afters
func (f *Fake) Now() time.Time
func (f *Fake) Since(t time.Time) time.Duration
func (f *Fake) After(d time.Duration) <-chan time.Time
func (f *Fake) Sleep(d time.Duration)
```

`Advance` must wake goroutines blocked in `After` when the fake clock passes their
deadline. This requires a sorted list of pending channels — the primary reason to prefer
`clockwork` over a naive hand-rolled implementation.

### Wiring into services

Add `Clock clock.Clock` to `server/internal/app/services/services.go`
(`ServicesList` struct). In `app.NewServer`, initialize with `clock.Real{}`. Tests
inject `clock.NewFake(time.Now())`.

The clock flows into incident service and job runners through the existing service
dependency injection pattern (constructor injection):

```go
// incidents/service.go
type Service struct {
    ...
    clock clock.Clock
}

func NewService(..., clk clock.Clock) *Service {
    return &Service{..., clock: clk}
}
```

### Phase 1 substitutions

Replace `time.Now()` calls in:

1. **`server/internal/handlers/incidents/service.go`** — ~30 call sites. Key ones:
   - `deriveCheckStatus` (L222): `time.Since(check.LastStatusAt)` → `svc.clock.Since(...)`
   - `confirmationElapsed` (L388): `time.Since(...)` → `svc.clock.Since(...)`
   - `recoveryElapsed` (L400): same
   - `handleFailure` (L329): `time.Now()` for `incident.StartedAt` → `svc.clock.Now()`
   - `calculateCooldown` (L493): `time.Now()` → `svc.clock.Now()`

2. **`server/internal/jobs/jobtypes/job_escalation_step.go`** — scheduling arithmetic.
   `startAt` is computed from `time.Now()` at job scheduling time; replace with clock.

3. **`server/internal/jobs/jobtypes/job_notification.go`** — any `time.Now()` for retry
   deadline or sent-at recording.

Calls in model constructors (`NewEscalationPolicyStep`, `NewIncident`, etc.) that set
`CreatedAt`/`UpdatedAt` use `time.Now()` for DB row timestamps — these should **not** be
clock-ified in Phase 1. They are not part of the state-machine logic and replacing them
would create subtle divergence between DB timestamps and business-logic timestamps.

### Fast-loop test harness integration

`server/test/integration/scenario/harness.go` (from spec 04) gains:

```go
type Scenario struct {
    ...
    Clock *clock.Fake
}
```

`NewPostgresScenario` wires `scenario.Clock` into the booted `app.Server` (via
`services.Registry.Clock`). Tests can then call `scenario.Clock.Advance(30 * time.Minute)`
to simulate an unacknowledged incident triggering a repeat cycle — no real sleep needed.

### New test enabled by this spec

`server/test/integration/scenario/ack_timeout_test.go`:

1. Create heartbeat check + escalation policy with `repeatAfterSeconds: 30` (post spec 05)
   and no auto-resolve.
2. `SendHeartbeat(status="down")` — incident opens, first page fires.
3. `scenario.Clock.Advance(31 * time.Second)` — triggers repeat-cycle job.
4. `WaitForWebhook(ctx, assertRepeatPage)` with a 2-second real-time deadline.
5. Verify two distinct `incident.paged` webhooks arrived.

This test would take 31 real seconds today; with a fake clock it takes ~200ms.

## Files to change

### New files

- `server/internal/utils/clock/clock.go` — `Clock` interface.
- `server/internal/utils/clock/clock_real.go` — `Real` implementation.
- `server/internal/utils/clock/clock_fake.go` — `Fake` implementation (or thin wrapper
  around `jonboulle/clockwork`).
- `server/internal/utils/clock/clock_test.go` — basic unit tests for `Fake.Advance`.
- `server/test/integration/scenario/ack_timeout_test.go` — the new test enabled by this
  spec (depends on spec 04 harness and spec 05 `repeatAfterSeconds`).

### Modified files

- `server/internal/app/services/services.go` — add `Clock clock.Clock` field.
- `server/internal/app/server.go` — initialize `Services.Clock = clock.Real{}`.
- `server/internal/handlers/incidents/service.go` — replace `time.Now()` / `time.Since()`
  with `svc.clock.Now()` / `svc.clock.Since()` at ~30 sites.
- `server/internal/jobs/jobtypes/job_escalation_step.go` — replace in scheduling
  arithmetic.
- `server/internal/jobs/jobtypes/job_notification.go` — replace in any non-row-timestamp
  uses.
- `server/test/integration/scenario/harness.go` (from spec 04) — add `Clock *clock.Fake`
  to `Scenario`, inject into booted server.
- `go.mod` / `go.sum` — add `github.com/jonboulle/clockwork` if using the library.

## Verification

```bash
# Build passes
make build

# Lint: no missed time.Now() calls in Phase 1 files (add a grep check in CI if desired)
make lint

# All existing tests still green (the Real clock is behaviourally identical to time.Now)
make test

# The new ack_timeout_test completes in under 1s of real time despite simulating 31s
make test-scenario
```

Manual check: run `make dev` and verify that a real incident still follows wall-clock
timing (i.e. `Real` wiring is correct). A 2-minute confirmation window should still take
~2 minutes in production.

## Risk log

| Risk | Mitigation |
|---|---|
| A `time.Now()` call is missed during Phase 1 substitution | Add a `grep -rn "time\.Now()" server/internal/handlers/incidents/ server/internal/jobs/jobtypes/job_escalation_step.go server/internal/jobs/jobtypes/job_notification.go` assertion in the PR checklist |
| `Fake.After` wakes goroutines in wrong order after `Advance` | Use `clockwork.FakeClock` which handles this correctly; add a test that advances past multiple pending `After` deadlines |
| Model `CreatedAt`/`UpdatedAt` constructors inadvertently clock-ified | Review: only replace `time.Now()` where it feeds business logic comparisons, not where it feeds DB row timestamps |
| Fake clock state leaks between parallel tests | Each `NewPostgresScenario` creates a fresh `clock.NewFake(time.Now())`; no shared state |

## Implementation Plan

1. Add `jonboulle/clockwork` to `go.mod` (or hand-roll `Fake` — decision to be made in
   implementation based on dependency appetite).
2. Write `clock.go`, `clock_real.go`, `clock_fake.go`, `clock_test.go`.
3. Add `Clock clock.Clock` to `services.go`; wire `clock.Real{}` in `server.go`.
4. Update `incidents/service.go` — replace all `time.Now()` / `time.Since()` that feed
   state-machine logic. Run tests after each method to catch regressions.
5. Update `job_escalation_step.go`.
6. Update `job_notification.go`.
7. Update scenario harness (`harness.go`) to inject `Fake` and expose `Advance`.
8. Write `ack_timeout_test.go`; run and verify it completes in under 1s.
9. Run `make test` — all existing tests must still pass (Real is transparent).
10. `make lint` — no remaining `time.Now()` in Phase 1 files.
