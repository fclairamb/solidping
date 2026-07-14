---
model: opus
effort: high
---

# Add a UI to configure aggregation / compaction parameters

## Problem

v0.3.0 introduced configurable aggregation/compaction parameters (retention and
roll-up windows for check results), but there is no dashboard UI to view or edit
them. Operators must set them through config / system parameters out of band.

Source issue: [#126 — Add an UI to setup configurable aggregation/compaction
parameters](https://github.com/fclairamb/solidping/issues/126) (labeled `bug`).

## Current state (verified against the code)

Aggregation is a **three-stage forward roll-up** of the `results` table (all tiers
share one schema, distinguished by `period_type`):

```
raw → hour → day → month
```

Note: the coarsest tier is **month**, and there is **no `week` period type**
(`models.PeriodType` is only `Raw` / `Hour` / `Day` / `Month`).

The three retention knobs already exist, and they are **already global**
(system-wide). Definitions:

- Struct `AggregationConfig` — [`server/internal/config/config.go:360`](server/internal/config/config.go)
  (mounted at `config.go:237`), defaults at `config.go:707`, validation
  (`>= 1`) at `config.go:1286`.
- System-parameter keys — [`server/internal/systemconfig/systemconfig.go:79`](server/internal/systemconfig/systemconfig.go),
  parameter defs at `systemconfig.go:855`.
- Roll-up job — [`server/internal/jobs/jobtypes/job_aggregation.go`](server/internal/jobs/jobtypes/job_aggregation.go)
  (stages at `:72`, `retentionFromConfig` at `:251`, boundary math at `:279`).
  One `JobTypeAggregation` job is provisioned **per org** at startup
  ([`job_startup.go:311`](server/internal/jobs/jobtypes/job_startup.go)); it
  self-reschedules ~hourly and loops immediately while there is backlog. The job
  reads retention from the single global `jctx.AppConfig.Aggregation` — there is
  **no per-org lookup**.

Two other consumers read the same global config to decide which tier to query:
[`handlers/badges/service.go:73`](server/internal/handlers/badges/service.go) and
[`handlers/statuspages/service.go:99`](server/internal/handlers/statuspages/service.go).

### What already works, and what's missing

- **API already exists.** A generic super-admin CRUD covers these keys today:
  `GET /api/v1/system/parameters`, `GET|PUT|DELETE /api/v1/system/parameters/:key`
  ([`app/server.go:928`](server/internal/app/server.go), guarded by
  `RequireAuth` + `RequireSuperAdmin`; service in
  [`handlers/system/service.go:167`](server/internal/handlers/system/service.go)).
  So this spec is **not** "add an API" — it is mostly a UI plus two backend
  hardening fixes below.
- **No per-key validation on write.** `SetParameter` only special-cases
  `auth.password.*` (`system/service.go:200`). A `PUT` of
  `aggregation.raw_retention_hours = 0` (or `-5`, or `"abc"`) is accepted and only
  rejected later at startup. This must be fixed (see decision 3).
- **Edits don't take effect without a restart.** The job reads the *static*
  startup-time `AppConfig`, populated once by `systemconfig.Service.Initialize`.
  A runtime `PUT` lands in the DB but the running job keeps using the old value
  until the server restarts. A settings UI that silently does nothing until a
  restart is a foot-gun; this must be fixed (see decision 4).
- **No UI.** There is no page to view/edit these values.

## Decisions (these resolve the previous open questions)

1. **Scope: global, super-admin only.** These are system-wide parameters
   (`organization_uid IS NULL`), not per-org. The UI belongs in the **server
   settings** area, not org settings: add an **"Aggregation"** tab to
   [`web/dash0/src/routes/orgs/$org/server.tsx`](web/dash0/src/routes/orgs/$org/server.tsx),
   cloning [`server.performance.tsx`](web/dash0/src/routes/orgs/$org/server.performance.tsx)
   (which already reads/writes numeric system parameters via `useSystemParameters()`
   / `useSetSystemParameter()`). Do **not** place it under
   `/orgs/$org/organization/*` — that area is per-org and non-admin-visible.

2. **No re-aggregation on change.** Changing a value **must not** trigger any
   re-processing of already-aggregated rows and must not recompute or reverse
   past roll-ups. The periodic job simply picks up the new thresholds on its next
   scheduled run and continues its normal forward-only roll-up. Two consequences
   to document on the page so they aren't surprises:
   - **Lowering** a window makes more not-yet-rolled-up rows eligible; the
     periodic job will roll them up over subsequent runs (this is normal forward
     aggregation, not a re-aggregation trigger).
   - **Raising** a window does **not** restore data that was already rolled up —
     once `hour → day` has happened the hourly rows are gone. Raising only affects
     rows not yet rolled up.

3. **Server-side validation.** Add validation for the three aggregation keys in
   `SetParameter` (mirror the `auth.password.*` special-case), returning
   `VALIDATION_ERROR` otherwise, with per-key floors:
   - `aggregation.raw_retention_hours` — integer `>= 1` (hours)
   - `aggregation.hourly_retention_days` — integer `>= 1` (days)
   - `aggregation.daily_retention_months` — integer `>= 1` (months)

   Extend the config-load validation at `config.go:1286` to match (it currently
   enforces a flat `>= 1` on all three), and keep the client-side validation in
   sync.

4. **Live apply (no restart).** The aggregation job must use the current
   DB-backed values on each run, not the frozen startup `AppConfig`. Preferred:
   at the start of `AggregationJobRun.Run`, read the three values from the
   systemconfig service (env override → DB row → default), replacing the direct
   `jctx.AppConfig.Aggregation` read in `retentionFromConfig`. Keep the env-var
   override winning (parity with `SetParameter` semantics). This is a localized
   change to the aggregation job only; the broader "all server settings need a
   restart" limitation is out of scope.

5. **Clearer parameter names.** The current names are ambiguous — `retention_hour`
   holds a value in *days*, `retention_day` holds *months*, and no name carries its
   unit. Rename all three keys, env vars, and Go struct fields to embed the unit
   and use adjective tier names:

   | Old key | New key | New env var | Go field (old → new) |
   |---|---|---|---|
   | `aggregation.retention_raw` | `aggregation.raw_retention_hours` | `SP_AGGREGATION_RAW_RETENTION_HOURS` | `RetentionRaw` → `RawRetentionHours` |
   | `aggregation.retention_hour` | `aggregation.hourly_retention_days` | `SP_AGGREGATION_HOURLY_RETENTION_DAYS` | `RetentionHour` → `HourlyRetentionDays` |
   | `aggregation.retention_day` | `aggregation.daily_retention_months` | `SP_AGGREGATION_DAILY_RETENTION_MONTHS` | `RetentionDay` → `DailyRetentionMonths` |

   Update the koanf tags, the `Key*` constants + parameter defs
   (`systemconfig.go:79`, `:855`), the struct + doc comments (`config.go:360`),
   defaults (`config.go:707`), validation (`config.go:1286`), the job
   (`job_aggregation.go`), and the read-side consumers (`badges`, `statuspages`).
   Because the feature only shipped in v0.3.0 this is an early rename: old env vars
   are dropped rather than aliased, and **no DB migration is needed** — any existing
   global `parameters` rows under the old keys are simply left orphaned (no longer
   mapped to a known key) and the renamed keys fall back to their defaults. Note the
   rename in the changelog.

## The three parameters

Each parameter is the **retention window for a source tier** — "keep this tier for
N, then roll older rows into the next tier." They map 1:1 to the transforms:

Names below are the **new** names (see decision 5 for the rename mapping).

| # | Transform | System-parameter key | Env var | Unit | Current default | Target default (this spec) |
|---|---|---|---|---|---|---|
| 1 | raw → hour | `aggregation.raw_retention_hours` | `SP_AGGREGATION_RAW_RETENTION_HOURS` | hours | 24 | **24** — unchanged |
| 2 | hour → day | `aggregation.hourly_retention_days` | `SP_AGGREGATION_HOURLY_RETENTION_DAYS` | days | 30 | **7** |
| 3 | day → month | `aggregation.daily_retention_months` | `SP_AGGREGATION_DAILY_RETENTION_MONTHS` | months | 12 | **2, min 1** |

Because the units differ per tier (hours / days / months), the UI must label each
field with its unit explicitly.

### Parameter 3: day → month, stays in months

No weekly tier, and **no unit change**. The coarsest tier stays **`month`**, the
third transform stays `day → month`, and `daily_retention_months` (renamed from
`aggregation.retention_day`) stays a **count of months**, so the existing monthly
roll-up logic is untouched. Only two things change:

- **Default: 12 → 2 months.** A value of N keeps the current partial month plus the
  N−1 most recent complete months as daily rows, rolling older complete months into
  the `month` tier. Default **2** therefore always retains at least one complete
  prior month of daily data (~30–60 days) before roll-up — the intent behind the
  earlier "at least ~31 days."
- **Floor stays `>= 1` month.** A user may set it to 1 (current partial month only)
  for more aggressive compaction.

**Keep the previous monthly aggregation logic.** Only the default at `config.go:707`
(12 → 2) and the doc comment at `config.go:360` change. The `day → month` boundary
math in `calculateAggregationBoundary`
(`startOfMonth.AddDate(0, -(retentionMonths-1), 0)`,
[`job_aggregation.go:279`](server/internal/jobs/jobtypes/job_aggregation.go)), the
month-bucket construction (`calculatePeriodBoundaries` for `month`,
`aggregateResults`, `buildAggregatedResult`), source-row deletion, and the "never
roll a partial month" invariant are all **kept exactly as-is**. This parameter is a
rename plus a default change, nothing more — the two read-side consumers
([`badges`](server/internal/handlers/badges/service.go),
[`statuspages`](server/internal/handlers/statuspages/service.go)) also keep their
months-based reads.

⚠️ **Retention drop to confirm.** Today's default is **12 months** of daily-
resolution history; **2 months** is a large reduction. This parallels the hour→day
30 → 7 drop. Confirm both are intended before merging — they materially reduce how
far back granular history is queryable.

## Proposal

1. **Backend — rename (do this first).** Rename the three keys, env vars, and Go
   fields per decision 5. No DB migration — any existing old-key `parameters` rows
   are left orphaned and the renamed keys fall back to their defaults. Everything
   below references the new names.
2. **Backend — validation.** Add per-key validation (decision 3) for the three
   renamed keys in `SetParameter` (`server/internal/handlers/system/service.go`),
   returning `VALIDATION_ERROR` with the standard error shape on bad input.
3. **Backend — live apply.** Make `AggregationJobRun.Run` read the current values
   from systemconfig each run (decision 4) so UI edits take effect on the next
   run without a restart.
4. **Backend — defaults.** Update per the table: `HourlyRetentionDays` 30 → 7;
   `DailyRetentionMonths` default 12 → 2 (still months, floor `>= 1`). Update the
   defaults at `config.go:707` and the doc comments at `config.go:360`. **No change
   to the aggregation/roll-up logic** — `daily_retention_months` keeps the existing
   month-based `calculateAggregationBoundary` and monthly bucketing.
5. **Frontend — UI.** Add an "Aggregation" tab to
   [`server.tsx`](web/dash0/src/routes/orgs/$org/server.tsx) and a
   `server.aggregation.tsx` route cloning
   [`server.performance.tsx`](web/dash0/src/routes/orgs/$org/server.performance.tsx):
   three labeled numeric fields (with units), read via `useSystemParameters()`,
   written per-key via `useSetSystemParameter()`. Build from the
   design-reference primitives
   (`http://localhost:4000/dash0/orgs/default/design-reference`); reuse the
   existing form patterns; fully responsive. Show the two behavior notes from
   decision 2 inline (no re-aggregation; raising a window doesn't restore data).
   Client-side validate each field's floor (all three `>= 1`, in their own unit).
6. **Tests.**
   - Backend handler tests (SQLite + Postgres parity per `server/CLAUDE.md`) for
     `SetParameter` on the three keys: valid write, and each invalid case
     (below floor, negative, non-integer) → `VALIDATION_ERROR`.
   - Backend test that the aggregation job honors an updated value **without a
     restart** (write via systemconfig, assert the next run uses the new
     boundary).
   - Playwright E2E in `web/dash0/e2e/` exercising the new tab: load, edit,
     save, reload-persists, and a validation error.

## Non-goals

- Per-org aggregation overrides (these stay global).
- A one-click "re-aggregate now" / bulk reprocessing action.
- Introducing a `week` period type — decided against; the coarsest tier stays
  `month`.
- Changing the monthly roll-up mechanism — `daily_retention_months` keeps the
  existing month-based logic entirely (boundary math, bucketing, "never roll a
  partial month"); only its default changes (12 → 2).
- Fixing the general "server settings need a restart" limitation for other
  parameter families — this spec only makes the three aggregation keys live.

## Implementation Plan

### Reconciliation note (spec premises vs. current code)

The spec's "Current state" is partly stale. A later spec (2026-07-11-16 / -17)
already added a **second, live** aggregation-retention key family and made the
job use it:

- `performance.aggregation_retention_raw_hours` (hours)
- `performance.aggregation_retention_hour_days` (days)
- `performance.aggregation_retention_day_months` (months)

`AggregationJobRun.Run` → `retentionFromConfig` already resolves these at
job-run time with precedence **env `SP_PERFORMANCE_*` → global DB parameter →
legacy koanf `aggregation.retention_*` → hardcoded default (24/30/12)** — i.e.
**decision 4 (live apply) is already implemented**, and there is a passing test
(`TestRetentionFromConfig_DefaultsAndDBParameter` / `_EnvOverrides`). These keys
also **already embed their unit** in the name, which is exactly the goal of
decision 5's rename.

Given that, introducing a third renamed `aggregation.raw_retention_hours`
family (decision 5) would be redundant and would create a key the running job
does **not** read — the opposite of the spec's own "no silent no-op" intent.
**Therefore this implementation builds the UI + validation on the existing live
`performance.aggregation_retention_*` keys** and does **not** perform the
literal key/env/struct rename. The legacy koanf `aggregation.retention_*`
fields stay as the deprecated fallback they already are.

### Steps

1. **Backend — validation (decision 3).** Add `IsAggregationRetentionKey` +
   `ValidateAggregationRetentionParameter` to `systemconfig` (integer, `>= 1`,
   reject non-integer / below-floor / negative). Wire into
   `handlers/system.Service.SetParameter` after the password check; the handler
   already maps `ErrInvalidParameter` → 422 `VALIDATION_ERROR`. Table test in
   `service_test.go` (valid + each invalid case).

2. **Backend — defaults (decision 4 / the table).** `hour→day` 30 → **7**,
   `day→month` 12 → **2** (raw stays 24). Update the live default constants in
   `job_aggregation.go` (`defaultRetentionHourDays`, `defaultRetentionDayMonths`),
   the koanf defaults in `config.go`, and the doc comments; fix
   `config_test.go`'s expected defaults. Add a test pinning the new default
   triple (24/7/2). No roll-up/boundary logic changes.

3. **Frontend — UI (decisions 1, 2, 5).** New
   `web/dash0/src/routes/orgs/$org/server.aggregation.tsx` cloning
   `server.performance.tsx`: three unit-labelled numeric fields (Raw hours /
   Hourly days / Daily months) read via `useSystemParameters()` and written
   per-key via `useSetSystemParameter()` against the `performance.*` keys;
   client-side floor `>= 1`; inline notes for the two decision-2 behaviours (no
   re-aggregation; raising a window doesn't restore rolled-up data). Add the
   "Aggregation" tab to `server.tsx` and i18n keys (all locales). Regenerate
   `routeTree.gen.ts`. Design-reference primitives, responsive.

4. **Tests.** Backend validation table test; backend default-triple test
   (live-apply itself is already covered). Playwright E2E in
   `web/dash0/e2e/server-admin.spec.ts`: load tab, edit, save, reload-persists,
   validation error.

5. **QA.** `make build-backend lint-back test`, `make build-dash0`,
   `cd web/dash0 && bun run lint`.
