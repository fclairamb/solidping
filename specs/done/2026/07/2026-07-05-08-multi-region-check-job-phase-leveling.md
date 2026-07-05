# Multi-region check jobs — durable phase leveling and bounded claim-ahead parking

## Context

User report on check `e2e55fa2-1719-49c3-b3f9-af0682db3b55` (org `webingenia`,
k8xp dev, base period 1 min, regions `default` + `eu-2` + `us-1`, so one
check_job per region with split period 3 min):

1. **us-1 stopped producing results** — silent from 11:23Z to 11:35Z
   (4 consecutive missed cycles) while the us-1 worker pod was Running the
   whole time and executing other checks.
2. **Regions are not leveled.** Expected: with base period *p* and *n*
   regions, job *i* fires at `t + i×p` (one result every *p*, round-robin
   across regions). Observed: `default` and `eu-2` fire **in the same
   second** (`:44.5` / `:44.2`), `us-1` drifted to an arbitrary `:52`.

## Diagnosis (verified live 2026-07-05 on k8xp, build `5dca14f5`, plus direct DB inspection)

Not hung checkers: the deployed build already contains specs `2026-07-05-04`
(mail deadlines) and `2026-07-05-05` (runner watchdog); worker stats showed
exec avg 327 ms and delay ≈ 0. Three scheduling defects compound instead:

### F1 — The region stagger exists only at job creation and is never maintained

`reconcileCheckJobs` staggers **new** jobs
(`server/internal/handlers/checks/service.go:1928`):

```go
scheduledAt := time.Now().Add(basePeriod * time.Duration(i))
```

but the update path for **existing** jobs (`service.go:1906-1925`) only
rewrites `period`/`config`/`plan_weight` — `scheduled_at` is never
re-leveled when the period or the region set changes.

### F2 — Any late run permanently destroys the phase

`calculateNextScheduledAt` (`server/internal/checkworker/worker.go:1227-1247`):

```go
nextScheduled := checkJob.ScheduledAt.Add(intervalDuration)
if nextScheduled.After(now) {
    return nextScheduled       // keeps phase
}
return now.Add(intervalDuration)  // late → re-anchors to now, phase lost
```

A server/worker restart makes *every* job late at once; the whole fleet then
re-anchors to the same claim batch timestamp → **lockstep**. Observed in DB:
`default` scheduled `11:44:44.477`, `eu-2` scheduled `11:44:44.143` — same
second, every cycle. `us-1`'s `:52` offset is not leveling either; it's
accumulated drift from late runs re-anchoring (`delay_ewma_ms` on the us-1
and eu-2 rows read **184,700 ms** — three minutes of recorded lateness).

### F3 — Claim-ahead parks runners for the whole sleep, starving the region

The claim gate is `scheduled_at <= now + FetchMaxAhead`
(`server/internal/checkworker/checkjobsvc/service.go:354`) with
`FetchMaxAhead` defaulting to **5 minutes**
(`server/internal/config/config.go:597`). After a job releases, its next
`scheduled_at` (+3 min) is already inside the window, so the next poll
re-claims it immediately — and the runner then **sleeps in-slot until
`scheduled_at`** (`worker.go:682-703`).

Observed effect on every region (28 jobs each): **22 jobs leased at any
instant on a 25-runner pool**, `freeRunners=3`. The worker log's
`Check job completed … duration_ms=179063` lines are ~177 s of in-runner
sleep plus a sub-second exec — the log measures from dispatch, before the
sleep, which is actively misleading during incident triage.

### How F1+F2+F3 produced the us-1 outage

Lockstep (F2) makes the whole 3-min cohort wake in the same second, execute,
release together, and immediately scramble to re-claim a parked slot (F3).
This check's exec on us-1 is ~880 ms — an order of magnitude slower than its
40–100 ms cohort neighbors — so it consistently releases last into a
zero-free-slot scramble and misses the wave. A missed job's `scheduled_at`
only advances on release, so it stays past-due but keeps losing claims to
the herd; observed 4 consecutive missed cycles (results at `11:20:52`, then
nothing until `11:35:52`), and each eventual late run re-anchored the phase
(F2), producing the `:52` drift.

## Design decisions

### D1 — Phase-locked rescheduling (leveling that survives restarts and lateness)

Replace the "late → `now + interval`" fallback with alignment to a
deterministic per-job phase. No schema change: derive the phase from data
the worker already has (jobs are returned with their check attached —
`attachChecks` in `checkjobsvc/service.go`):

```
basePeriod = check.Period                       // pre-split period
i          = index of job.Region in sorted(check.Regions)   // 0 if none
jitter     = hash(check.UID) mod basePeriod     // spreads different checks
phase      = jitter + i × basePeriod            // spreads a check's regions
next       = smallest t > now with (t − unixEpoch) ≡ phase (mod job.Period)
```

Properties:
- Region *i* always fires `i × basePeriod` after region 0 — the exact
  leveling the user expects — and different checks are spread across the
  base period by the UID jitter instead of herding on wall-clock ticks.
- A late or missed run resumes **at the next phase-aligned tick**: no drift,
  no lockstep, self-healing after restarts (F2 fixed).
- Deterministic across processes; remote and in-process workers agree.

`calculateNextScheduledAt` implements this for both release paths
(`ReleaseLease` at `worker.go:1123`, `ReleaseLeaseWithSchedulingState` at
`worker.go:970`). Callers that lack the attached check (stuck-job reaper,
rate-limit deferral) may keep `now + period` — the next normal run re-aligns.

Edge cases: region not found in `check.Regions` (stale job about to be
reconciled away) → `i = 0`; single-region and no-region jobs get jitter-only
phase, which also de-herds them.

### D2 — Reconcile levels existing jobs, not just new ones

In `reconcileCheckJobs`:
- Iterate regions in **sorted order** so index *i* is stable and matches D1.
- When `needsUpdate` includes a period change or the region set changed
  (job added/removed), also `Set("scheduled_at", …)` /
  `Set("effective_scheduled_at", …)` to each job's next phase-aligned tick
  per D1, so an edit immediately restores leveling instead of waiting for
  organic convergence.
- New jobs use the same phase formula (replacing the current
  `time.Now().Add(basePeriod × i)` one-shot stagger).

### D3 — Bound claim-ahead parking

Keep claim-ahead (it is what makes firing punctual), but stop letting it
consume the pool for minutes. Clamp the eligibility window **per job**:

```
window = min(FetchMaxAhead, job.Period / 2, 30s)
```

applied in `selectAvailableJobs` (the `scheduled_at <= now + maxAhead` gate
becomes per-row: `scheduled_at <= now + LEAST(…)` or computed Go-side per
lane query). A runner then parks ≤ 30 s per job instead of ≤ 5 min; with 28
jobs/region the steady-state parked count drops from ~22 to ~3, restoring
real free capacity between waves (F3 fixed). D1 already removes the herd, so
the scramble this capacity used to lose disappears too.

Config note: `fetch_max_ahead` stays as an operator override; the koanf
`SP_*` env quirk for multi-word keys applies if defaults are touched.

### D4 — Honest execution logging

`Check job completed` (`worker.go:827`) must log the **exec** duration;
report the pre-schedule sleep and the past-due delay as separate fields
(`wait_ms`, `delay_ms`). The current conflated number (`duration_ms≈179000`
for healthy sub-second checks) sent this investigation down a hung-checker
path the fleet had already fixed.

### D5 — Visibility on parking

Add the parked count (claimed, sleeping, not yet due) to the worker's
`Processing stats` log line and as a gauge
(`solidping_check_runner_parked`), next to the existing `freeRunners`.

## Non-goals

- Lane/WFQ redesign, reaper timing, per-region capacity autoscaling.
- Moving the sleep out of runners entirely (time-heap dispatch so parked
  jobs don't occupy runner slots at all). That is the structurally cleaner
  end-state for F3; D3 makes it unnecessary at current fleet sizes — leave
  it to a future spec if regions grow past a few hundred jobs.

## Acceptance criteria

1. Fresh check, base period 1 min, 3 regions: results arrive ~every minute
   rotating `default → eu-2 → us-1`, offsets `i × 1 min` within a few
   seconds — and the pattern **still holds after restarting the server and
   both region workers** (F2 regression).
2. Editing the check's period or regions immediately re-levels all its jobs
   (D2), verified via `check_jobs.scheduled_at` spacing.
3. A job that misses a cycle (simulated full pool) runs at the next
   phase-aligned tick; its subsequent runs stay on the original phase (no
   `:44 → :52`-style drift).
4. With ~28 jobs/region and default config, leased-but-sleeping jobs stay
   bounded by the D3 window (spot-check: leased count near poll churn, not
   near pool size; `freeRunners` ≈ pool size between waves).
5. `Check job completed` logs sub-second `duration_ms` for sub-second
   checks, with sleep/delay in their own fields (D4).
6. Tests: table-driven unit tests for the phase computation (on-time, late,
   restart, region added/removed, region missing from check, no-region job,
   jitter determinism); reconcile re-leveling tests; claim-window clamp
   tests against **both Postgres and SQLite** backends.

## Verification on k8xp after deploy

`default`/`eu-2`/`us-1` results for check `e2e55fa2` spaced ~60 s apart per
region-rotation; `check_jobs` rows show distinct phase-aligned
`scheduled_at`; us-1 misses no cycle over an hour of observation despite the
region's historical saturation.

## Implementation Plan

Code surface confirmed by reading `server/internal/db/models/check_job.go`
(`CheckJob.Check *Check` populated at claim time by `attachChecks` in
`checkjobsvc/service.go`) and `check.go` (`Check.Regions []string`,
`Check.Period timeutils.Duration`) — the phase formula's inputs
(`check.Regions`, `check.Period`, `check.UID`) are all available on
`checkJob.Check` with no schema change, exactly as D1 assumes.

### 1. Phase package (new, shared by D1 + D2)

Add `server/internal/checkworker/scheduling/phase.go` (package `scheduling`,
already imported by both `worker.go` and `service.go`) with:

- `JitterFor(checkUID string, basePeriod time.Duration) time.Duration` —
  `hash(checkUID) mod basePeriod` using `hash/fnv` (fnv-1a 64-bit; stdlib, no
  new dependency, sufficient for a non-cryptographic spreading hash).
- `RegionIndex(region *string, regions []string) int` — index of `region` in
  a **sorted copy** of `regions` (sort inside this helper so every caller —
  worker release path, reconcile — agrees on the same order without having to
  remember to pre-sort); returns 0 if `region` is nil or not found (stale job
  / no-region job, per spec edge cases).
- `NextAligned(now time.Time, basePeriod, jobPeriod time.Duration, checkUID string, region *string, regions []string) time.Time`
  — computes `phase = JitterFor(checkUID, basePeriod) + RegionIndex(...) × basePeriod`,
  then the smallest `t > now` with `(t.Unix()) ≡ phase (mod jobPeriod)`
  (using Unix seconds as the epoch anchor — sub-second precision isn't needed
  for minute-scale periods and this keeps the mod arithmetic simple and
  identical across processes). Guard `basePeriod <= 0` / `jobPeriod <= 0` by
  falling back to `now.Add(jobPeriod)` (defensive; reconcile never produces
  zero periods).

Table-driven unit tests in `phase_test.go`: jitter determinism (same UID +
period → same jitter, different UIDs → spread), region index (found /
missing / nil-region / nil-regions-slice), and `NextAligned` on-time / late /
restart-after-long-gap / no-region / single-region cases — pure functions,
no DB needed.

### 2. D1 — worker.go `calculateNextScheduledAt`

Replace the body (lines 1227-1247) to use `checkJob.Check` when present:

```go
func (r *CheckWorker) calculateNextScheduledAt(checkJob *models.CheckJob) time.Time {
    now := time.Now()
    jobPeriod := time.Duration(checkJob.Period)

    if checkJob.Check != nil {
        basePeriod := time.Duration(checkJob.Check.Period)
        if basePeriod > 0 && jobPeriod > 0 {
            return scheduling.NextAligned(
                now, basePeriod, jobPeriod, checkJob.CheckUID, checkJob.Region, checkJob.Check.Regions,
            )
        }
    }

    // No attached check (defensive — claim always attaches one) or a zero
    // period: fall back to the old anchor-preserving/late-catchup logic.
    if checkJob.ScheduledAt == nil {
        return now.Add(jobPeriod)
    }
    if next := checkJob.ScheduledAt.Add(jobPeriod); next.After(now) {
        return next
    }
    return now.Add(jobPeriod)
}
```

Both `releaseLease` (worker.go:1119) and `releaseLeaseWithCost` (worker.go:958,
via `calculateNextScheduledAt` at line 964) call this unchanged — no signature
change, so both release paths get phase-locking for free. `backend/direct.go`
and `handlers/workers/service.go` have their own `calculateNextScheduledAt`
copies operating on a bare `*models.CheckJob` with `Check` never attached
(confirmed: both fetch the job via a plain `NewSelect().Model(&job)`, no
`attachChecks`) — these are exactly the "callers that lack the attached
check" the spec grandfathers to keep `now + period`; left unchanged, not
silently broken (nil-Check is handled by the fallback above if these ever
gain phase-locking later).

Rewrite `TestCalculateNextScheduledAt` in `worker_test.go` (lines 73-149) to
cover: on-time (Check attached, still resolves to next phase tick, not a
literal `scheduled_at+period`), late/behind-schedule (resumes at next
phase-aligned tick, not `now+period`), restart-style long gap (multiple
periods missed, lands back on the original phase), region added/removed
(index shifts, still phase-aligned), region missing from `check.Regions`
(falls back to i=0), no-region job (jitter-only), no attached Check
(defensive fallback path), and jitter determinism (two jobs with different
CheckUIDs get different phases; same UID+region always the same phase).

### 3. D2 — reconcileCheckJobs re-leveling

In `server/internal/handlers/checks/service.go`:

- Sort `targetRegions` (copy, `sort.Strings`) before the `for i, region :=
  range targetRegions` loop (currently line 1905) so index `i` is stable and
  matches `scheduling.RegionIndex`'s own sort — both must agree, so
  `RegionIndex` sorting its input again on already-sorted data is a cheap
  no-op, not a mismatch.
- New-job path (line 1928): replace `scheduledAt :=
  time.Now().Add(basePeriod * time.Duration(i))` with
  `scheduledAt := scheduling.NextAligned(time.Now(), basePeriod, time.Duration(splitPeriod), check.UID, &regionCopy, targetRegions)`.
- Existing-job update path (lines 1906-1925): extend `needsUpdate` to also
  fire when the region set membership changed even without a period/config
  change (add/remove of a sibling region shifts every survivor's index) —
  track this via a `regionSetChanged bool` computed once per call (compare
  the sorted existing-region-set to the sorted target-region-set) and OR it
  into each job's `needsUpdate`. When `needsUpdate` is true, also
  `Set("scheduled_at", …).Set("effective_scheduled_at", …)` to
  `scheduling.NextAligned(time.Now(), basePeriod, time.Duration(splitPeriod), check.UID, &region, targetRegions)`
  in the same UPDATE.
- No-region path (lines 1861-1878, single job): use
  `scheduling.NextAligned(time.Now(), basePeriod, basePeriod, check.UID, nil, nil)`
  instead of `now` — still jitters the single job so identical checks don't
  herd on creation, per D1's "single/no-region jobs get jitter-only phase".

Tests in `handlers/checks/service_test.go` (table-driven, extending existing
reconcile tests): editing period re-levels `scheduled_at` for all regions;
adding a region shifts existing survivors' phase (index changes) and gives
the new region its own slot; removing a region re-levels the survivors;
untouched fields (config-only edit with same period/regions) do NOT rewrite
scheduled_at when nothing that affects phase changed — assert `needsUpdate`
is false and `scheduled_at` is untouched in that case, so this isn't a
regression on every unrelated edit.

### 4. D3 — bounded claim-ahead window

In `checkjobsvc/service.go`, `selectAvailableJobs` (line 328): change the
static `Where("scheduled_at <= ?", now.Add(maxAhead))` (line 354) to a
per-row bound. Since `period` is stored as a Postgres `interval` / SQLite
text via `timeutils.Duration`'s `Value()`/`Scan()` (confirmed — no existing
cross-dialect interval arithmetic helper exists in this codebase), doing the
clamp in SQL would need dialect-specific interval math on both backends.
Instead, following the spec's explicitly-allowed alternative ("computed
Go-side per lane query"), add a second WHERE gate using period converted to
seconds via a portable expression both dialects support:

```go
Where("scheduled_at <= ?", now.Add(maxAhead)).
Where("scheduled_at <= ? + LEAST(period_seconds_expr, 30)", now)
```

On reflection this still needs per-row period comparison, which is exactly
what SQL is good at once period is available as a plain numeric column — but
`period` is stored as an interval/text type, not a numeric column, on this
schema. Given the dual-backend risk, implement the clamp as a **second
query-time filter applied after the fetch, before dispatch**, i.e. compute
`effectiveMaxAhead` per lane call as `min(FetchMaxAhead, 30s)` for the SQL
gate (this bounds the *worst case* — most jobs have periods ≪ 2×30s so this
alone fixes the common multi-region case where periods are 1-5 min), AND
add a per-row post-filter in Go immediately after `query.Scan(ctx)`: drop
(and simply not include in the returned slice) any job whose
`scheduled_at > now + min(maxAhead, job.Period/2, 30s)` — these jobs were
fetched because they satisfied the coarse SQL gate but not their own tighter
per-job bound, so they're released back for the next poll instead of being
claimed. This keeps the SQL simple/portable and the per-job clamp exact, at
the cost of occasionally fetching (but not claiming) a few extra rows within
the coarse window — acceptable since `limit` already bounds the query cost
and this only matters when `job.Period/2 < min(FetchMaxAhead, 30s)`, i.e.
sub-minute jobs.

Concretely: `selectAvailableJobs` gains a `now.Add(min(maxAhead, 30*time.Second))`
gate (tightening the existing flat 5-minute default without needing config
changes for the common case), and after `query.Scan(ctx)` populates `*jobs`,
filter the slice in place, keeping only jobs where
`job.ScheduledAt.Before(now.Add(min(maxAhead, job.Period/2, 30s)))`. Add a
small helper `clampAhead(maxAhead time.Duration, period timeutils.Duration) time.Duration`
in the same file.

Tests: table-driven claim-window clamp tests in `service_test.go` against
BOTH SQLite (existing `setupTestDB` helper) and embedded Postgres (new
`_postgres_test.go` file mirroring `costdist_postgres_test.go`'s
self-skip-under-`-short` + distinct port pattern — port `15437`, since
`15434`/`15436` are already used by `costdist_postgres_test.go` /
`notifier/postgres_test.go`): a job with a 1-minute period and
`scheduled_at` 40s out is NOT claimed (window clamps to 30s), a job with a
10-minute period and `scheduled_at` 20s out IS claimed (well within its
5-minute-period/2 bound and the 30s floor), and the existing
`FetchMaxAhead`-only behavior is preserved when it's already tighter than
the per-job clamp (small `FetchMaxAhead` override still wins).

### 5. D4 — honest execution logging

In `worker.go`, `executeJob` (lines 826-829): the existing `delay` variable
(computed at line 684-687 from `sleepTime := checkJob.ScheduledAt.Sub(startTime)`)
is the wait; `duration := time.Since(startTime)` conflates wait + exec. Add
an `execDuration := time.Since(execStart)` (execStart already captured at
line 739) and change the final log to:

```go
logger.InfoContext(ctx, "Check job completed",
    "status", result.Status,
    "duration_ms", execDuration.Milliseconds(),
    "wait_ms", sleepDuration.Milliseconds(),
    "delay_ms", delay.Milliseconds())
```

where `sleepDuration` is the actual observed sleep (`max(sleepTime, 0)`,
captured once near line 690 instead of only using the raw `sleepTime` which
can be negative). Since `sleepTime`/`delay` are computed before the
rate-limit-gate `return` at line 718 (a different code path that doesn't
reach this log line), no signature changes ripple elsewhere. Add/extend a
unit test asserting the logged fields split correctly (using a test logger
handler capturing attrs, following whatever pattern — if any — the codebase
already uses for slog assertions; otherwise assert via the underlying
`AverageDelayMs`/duration values already covered by `TestDelaySampleMs`, and
add a small helper-level test for the wait/delay separation math itself
rather than parsing log output if no slog-capture helper exists).

### 6. D5 — parked visibility

Add a `parked atomic.Int32` counter to `CheckWorker` (next to
`availableRunners`), incremented right before the pre-schedule sleep begins
(`executeJob`, just before the `if sleepTime > 0 { timer := ... }` block at
line 690) and decremented right after the timer fires or the sleep is
skipped (i.e. wraps the existing sleep block so both the timer-fired and
`sleepTime <= 0` paths decrement exactly once — use a small
`defer`-friendly pattern: increment unconditionally on entry to the
sleep-accounting section, defer the decrement).

Expose it the same way `availableRunners` is exposed to `stats.ProcessingStats`
via `SetFreeRunnersFunc`: add `stats.SetParkedFunc(fn func() float64)`
(mirroring `SetFreeRunnersFunc`) in `stats/processingStats.go`, add a
`Parked float64` field to `ReportedStats`, thread it through `report()`
into both the `"Processing stats"` log line (new `slog.Float64("parked",
...)` attribute) and a new Prometheus gauge:

```go
// metrics.go, next to WorkerFreeRunners
WorkerParked = prometheus.NewGaugeVec(
    prometheus.GaugeOpts{
        Name: "solidping_check_runner_parked",
        Help: "Runner slots currently occupied by a claimed job sleeping until its scheduled time",
    },
    []string{"worker_uid", labelRegion},
)
```

with a `SetWorkerParked(workerUID, region string, count float64)` recording
helper mirroring `SetWorkerFreeRunners`, registered alongside it in the
`MustRegister` call (metrics.go ~line 403), and wired in `reportStats`
(worker.go ~1314) the same way free runners already is. Unit test: a
fake/slow checker held in its sleep window bumps the parked gauge/counter
and releases it back to 0 after the timer fires (extends the existing
runner-loop test harness rather than adding a new one).

### 7. QA order

Implement 1 → 2 → 3 → 4 → 5 → 6 as separate commits (each is independently
testable), running `make fmt` after each. Then `make build-backend lint-back
test` at the end, iterating on failures. `make test` runs `go test
./... -short`, so the new embedded-Postgres test must self-skip under
`-short` exactly like `costdist_postgres_test.go` — it will not run in the
coordinator's QA loop by default, only under a full (non-short) run; note
this explicitly in the final report rather than claiming full dual-backend
CI coverage.
