---
model: opus
effort: high
---

# Multi-region checks: each region should run at the full check period, with a configurable inter-region offset

## Problem

Today, selecting N regions on a check **divides** the effective per-region
frequency instead of multiplying coverage. The reconcile path creates one
`check_jobs` row per region with a *split* period of `basePeriod × n`
([service.go:2063](server/internal/handlers/checks/service.go:2063)):

```go
splitPeriod := timeutils.Duration(basePeriod * time.Duration(n))
```

and the phase formula staggers region *i* by `i × basePeriod`
([phase.go:107](server/internal/checkworker/scheduling/phase.go:107)):

```go
phase := (jitter + time.Duration(i)*basePeriod) % jobPeriod
```

So a 1-minute check on 3 regions runs each region every **3 minutes**
(+0 min / +1 min / +2 min), for a combined rate of one execution per minute.

The desired semantics — and what users (and every competitor: Checkly,
Pingdom, UptimeRobot) expect — is that the period applies **per region**: a
1-minute check on 3 regions runs every minute *in each region* (3 executions
per minute total), staggered across the period.

## Proposal

### 1. Per-region full period

In `reconcileCheckJobs`
([service.go:1959](server/internal/handlers/checks/service.go:1959)):
each region's job gets `period = basePeriod` (drop `splitPeriod` entirely —
job period and base period become the same value for all region counts).

### 2. Inter-region offset ("spread"), default `period / n`

Regions must not all fire on the same second by default. Replace the
`i × basePeriod` stagger with `i × spread`, where:

- **Default**: `spread = basePeriod / n` — a 1-minute check on 3 regions
  fires at jitter+0s, +20s, +40s. Even coverage; the org effectively gets a
  global detection interval of `period / n`.
- **Override**: a new optional per-check field `regionSpread`
  (duration, camelCase in API JSON) lets the user force e.g. a 1-second
  spread so all regions sample near-simultaneously (comparative
  cross-region latency, fast multi-region confirmation of an outage).
  **Decided: first-class column on `checks`** (nullable duration, same
  storage convention as `checks.period`), NOT a `checks.config` key — it is
  scheduling input that drives `check_jobs` creation, not checker config.
  Migration on both PostgreSQL and SQLite.
- **Validation**: `0 ≤ regionSpread < period`. Reject otherwise with
  `VALIDATION_ERROR`. Absent/null = default behavior.

Phase formula in `NextAligned`
([phase.go:93](server/internal/checkworker/scheduling/phase.go:93)) becomes
`phase = (jitter + i × spread) % jobPeriod`. `NextAligned`'s signature gains
the spread (or the already-resolved per-region offset); **both** call sites —
reconcile ([service.go:2081](server/internal/handlers/checks/service.go:2081),
[service.go:2112](server/internal/handlers/checks/service.go:2112)) and the
worker's release/reschedule path — must resolve the same spread from the same
inputs, since the phase must be reproducible across processes (that is the
whole point of spec 2026-07-05-08 D1). The worker computes phase from the
job's attached check, so the spread must be derivable there too (from
`check.Period`, `len(regions)`, and the new field). Update the package
comment block in phase.go which documents the old `i × basePeriod` scheme.

Decision (honest opinion, discussed with Florent): yes to the override, but
keep it **API-first / advanced**. The scheduler cost is near-zero (one term
in the phase formula); the real cost is UI surface. Default stays
`period / n`. A free-form duration beats an enum (`spread|simultaneous`)
since the user explicitly wants to pick values like 1s.

### 3. Entitlements accounting must count regions

`ChecksPerMinute` usage is currently `sum(60s / period)` per enabled check,
**ignoring regions** ([usage.go:12](server/internal/entitlements/usage.go:12))
— which matches actual execution today, but after this change actual
execution becomes `n × 60s / period`. Without a fix, a 3-region check
under-counts 3× against `maxChecksPerMinute`
([defaults.go:67](server/internal/entitlements/defaults.go:67), SaaS default
6/min). Update the usage computation to multiply by `max(1, len(regions))`.
The worker-side dispatch gate
([worker.go:870](server/internal/checkworker/worker.go:870)) counts real
executions and needs no change, but verify usage-page numbers and quota
errors agree with it.

### 4. Migration of existing jobs

`reconcileCheckJobs` only runs on check create/update
([service.go:1451](server/internal/handlers/checks/service.go:1451)) — no
startup pass. Existing multi-region jobs would keep their old
`basePeriod × n` period until someone edits the check. Add an idempotent
one-time startup reconcile (pattern: the connection-URL startup reconcile at
[server.go:2311](server/internal/app/server.go:2311)) that recomputes
`period`, `scheduled_at`, and `effective_scheduled_at` for every job whose
period no longer matches its check's period, on both PostgreSQL and SQLite.

### 5. Sub-minute lane interaction

Under the old scheme a 30s check on 3 regions produced 90s jobs; now it
produces three 30s jobs, which enter the sub-minute lane. Review the lane
comments and heuristics that reason about "split periods"
([checkjobsvc/service.go:512](server/internal/checkworker/checkjobsvc/service.go:512),
[:521](server/internal/checkworker/checkjobsvc/service.go:521)) and the
recently-added next-eligible wake hint (commit 27c0e3a7) — behavior should
already be correct since it keys off the job's actual period, but the
comments describe the old world and the load assumptions change.

### 6. Frontend + docs

- Regions selector on the check edit page: add a hint line making the new
  semantics explicit ("each selected region runs the check every
  {period}"). All locales (en/fr/de/es).
- Optional: expose `regionSpread` as an advanced field — can be deferred;
  API-first is fine for this spec.
- Usage page: verify the checks/minute figure reflects the region
  multiplier, and add one sentence stating that multi-region checks consume
  `regions × 60s / period` checks per minute (all locales).
- Docs site (`web/docs/`): update any page describing multi-region
  scheduling; same one-line callout about the `n ×` consumption in the
  plans/limits docs.

### 7. Tests

- `phase_test.go`: new-formula phase leveling, spread default and override,
  spread=0/negative/≥period validation, cross-process reproducibility
  (reconcile vs worker path compute identical phases).
- Reconcile tests: period no longer multiplied; region add/remove
  re-levels with the new spread; `regionSpread` persisted and applied.
- Entitlements usage tests: region multiplier in `ChecksPerMinute`.
- Startup reconcile: idempotent, fixes stale split-period jobs, both DBs.

## Decisions (settled with Florent, 2026-07-20)

- `regionSpread` is a **first-class column on `checks`**, not a
  `checks.config` key — it drives `check_jobs` creation (scheduling input,
  not checker config). Requires a migration on both PostgreSQL and SQLite.
- The usage page and plan docs **do** get the one-sentence callout that
  multi-region checks consume `n ×` checks/minute.

## Implementation Plan

Re-verified line numbers against current source (they had drifted). Current
released migration is `006_v0_5_0` (manifest `0.5.0`), so the new column ships
in a fresh `007_v0_6_0` migration on both backends.

### Backend

1. **Migration `007_v0_6_0`** (postgres `interval`, sqlite `text`, both nullable):
   `alter table checks add column region_spread …`. Down drops it.
2. **`db/models/check.go`**: add `RegionSpread *timeutils.Duration`
   (`bun:"region_spread,nullzero"`) to `Check`; add `RegionSpread` +
   `ClearRegionSpread` to `CheckUpdate`; add `Regions []string` to `CheckRate`;
   add `Check.RegionSpreadDuration() *time.Duration` helper.
3. **`scheduling/phase.go`**: `NextAligned` gains a `spread time.Duration`
   param; `phase = (jitter + i*spread) % jobPeriod`. New `RegionSpread(base, n,
   override)` resolver — default `base/n`, `override` verbatim, `0` when
   `n<=1`/`base<=0`. Rewrite the package doc comment (i×basePeriod → i×spread).
4. **`handlers/checks/service.go`**: `reconcileCheckJobs` drops `splitPeriod`
   (job period = `check.Period`), resolves `spread` once, passes it to every
   `NextAligned`; `needsUpdate` compares `existing.Period != check.Period`.
   Create/Update parse+validate `regionSpread` (`0 ≤ spread < period`,
   `VALIDATION_ERROR`); reconcile also runs when only `regionSpread` changes.
   `CheckResponse`/`convertCheckToResponse` echo `regionSpread`. Add
   `ReconcileStaleJobSchedules(ctx)` (startup pass).
5. **`db/postgres|sqlite`**: `createCheckJobs` drops split period (job period =
   `check.Period`) and staggers the initial `scheduled_at` by `spread*i`
   (region 0 stays at `now` — preserves the check-created express path, see the
   reconcile_test scope note). `UpdateCheck` sets/clears `region_spread`.
   `ListOrgCheckRates` selects `regions`. New `ListChecksWithStaleJobPeriods`.
6. **`entitlements/usage.go`**: `ChecksPerMinute += (60s/period) × max(1,
   len(regions))`.
7. **`checkworker/worker.go`**: `calculateNextScheduledAt` resolves the spread
   from the attached check and passes it to `NextAligned`.
8. **`checkjobsvc/service.go`**: refresh the stale "split period" lane comments.
9. **`app/server.go` + `main.go`**: `Server.ReconcileCheckJobSchedules` called
   once at startup (after encryption auto-migrate), idempotent.

### Frontend + docs

10. Check form: regions hint "each selected region runs the check every
    {period}" via `t("form.regionsHint")`; add key to en/fr/de/es `checks.json`.
11. Usage page: muted note that multi-region checks consume `regions × 60s /
    period`; add `usage.multiRegionNote` to en/fr/de/es `org.json`.
12. `web/docs/`: same callout on the multi-region / plans-limits pages.

### Tests

13. `phase_test.go`: thread `spread` through existing calls; new subtests for
    the default (`base/n`) and override spreads, `spread=0`, and the
    `RegionSpread` resolver; deterministic/reproducible checks.
14. `reconcile_test.go`: update assertions to the un-multiplied period + spread
    leveling; new `regionSpread` persisted/applied + validation tests.
15. `entitlements/service_test.go`: `Usage` region multiplier.
16. New startup-reconcile test (idempotent, fixes stale split-period jobs).
17. `worker_test.go`: thread the resolved spread through the `want` calls.
