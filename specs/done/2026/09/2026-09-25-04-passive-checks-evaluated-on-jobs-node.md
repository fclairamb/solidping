---
model: opus
effort: high
---

# Passive checks are evaluated inside a region: a dead region silences them, and an agent region turns them into errors

## Problem

Heartbeat and email checks are **passive**: they make no outbound request, they
only wait for a signal (`checkerdef/types.go:245-256`). Yet their "is the signal
overdue?" evaluation runs as an ordinary regional check job:

- On create, a passive check gets regions from `ResolveRegionsForCheck` like any
  other check ([regions.go:248-286](server/internal/regions/regions.go#L248)).
  `createCheckJobs` then materializes one `check_jobs` row per region
  ([postgres.go:1654-1704](server/internal/db/postgres/postgres.go#L1654)).
  The dash0 form even shows the region picker for heartbeats (`check-form.tsx:1588`).
- A regional worker claims the job and runs `executePassiveJob`
  ([worker.go:960-964, 1694-1745](server/internal/checkworker/worker.go#L960)).
  That reads the last signal (`LastSignals`: newest raw row with
  `worker_uid IS NULL`) and applies `passiveEvaluation`
  ([worker.go:1611-1689](server/internal/checkworker/worker.go#L1611)):
  - up within 1× period → Up;
  - older → Down "Heartbeat overdue";
  - running within 2× period → Running, otherwise Timeout;
  - no signal → Down.

This causes three real failures:

1. **A dead region silences the dead-man's switch.** On 2026-09-24 the
   `lauterbourg` region was dark for 8 hours. A heartbeat check pinned there
   would never have reported its sender as missing: the evaluator died with the
   region. For a feature whose whole point is "tell me when something stops",
   this is the worst failure mode. The operator had to work around it by
   pinning the new per-host heartbeat checks to regions running on *other*
   hosts.
2. **Agent regions turn every evaluation into an error and an incident.**
   - Agents refuse passive evaluation: `LastSignals` returns
     `ErrPassiveUnsupported` on the WebSocket backend
     ([backend/ws.go:389-393](server/internal/checkworker/backend/ws.go#L389)).
   - Neither the agent claim query
     ([checkjobsvc/service.go:484-533](server/internal/checkworker/checkjobsvc/service.go#L484))
     nor `handleClaim`
     ([agentws/handler.go:802-902](server/internal/handlers/agentws/handler.go#L802))
     filters by check type.
   - So a heartbeat in a private region, or in a shared cloud region whose job
     a **system agent** wins (e.g. kansas-city, tokyo), submits an Error result
     every period ("passive checks are not supported on deported agents").
     `ProcessCheckResult` then opens an incident.
   - If a private region is in the org's `default_regions`, every new heartbeat
     lands there. No test covers heartbeats on agents.
3. **Regions add nothing but duplicates.** With N regions, N workers write N
   identical evaluation rows per period, each running incident processing.
   The evaluation reads the same database row whichever region runs it.

## Proposal

### 1. Passive checks have no regions

- Normalize `regions` to empty for passive types on create and update, in the
  API, the MCP tools and config-as-code alike. An explicit list is accepted and
  dropped, not rejected, so existing config-as-code files keep applying. The
  response and the config-as-code diff show the empty list.
- Skip passive types wherever jobs are materialized per region:
  - `createCheckJobs` (Postgres and SQLite);
  - `reconcileCheckJobs`
    ([service.go:2886-3088](server/internal/handlers/checks/service.go#L2886));
  - the boot repair `ReconcileStaleJobSchedules` / `ListChecksWithStaleJobRegions`
    ([region_migration.go:54-84](server/internal/db/postgres/region_migration.go#L54)),
    which would otherwise recreate the regional jobs at every start.
- dash0: hide the region picker for passive types. The passive list is
  duplicated in `check-form.tsx:155` and `lib/check-scheduling.ts:36`; derive
  both from one place.
- Migration: set `regions = '{}'` on existing passive checks and delete their
  regional `check_jobs`.

### 2. One evaluation per period, on the jobs node

Evaluate passive checks on the jobs node, which is region-independent. Exactly
one evaluation runs per check per period.

Recommended shape: keep **one `check_jobs` row per passive check with a NULL
region**, and make the jobs node its only claimer.

- Cloud workers and agents exclude passive types from every claim query. This
  also closes failure 2 even for rows that escape the migration.
- A `passive_eval` job on the jobs node claims due passive jobs with the normal
  lease, runs the existing `passiveEvaluation`, and writes the result through
  the same path as today (`ProcessCheckResult`).
- Leases make it safe with several jobs nodes. `scheduled_at` / period handling
  is reused as is.

A dedicated sweep with its own schedule column is an acceptable alternative.
The requirements are the same either way:

- It does not depend on any region or check worker being alive.
- A jobs node restart loses nothing (the next run evaluates whatever is due).
- Two jobs nodes never evaluate the same check twice in one period.

**Result marker.**
- Today `LastSignals` tells signals from evaluations by `worker_uid IS NULL`.
- The jobs node has no `workers` row, and `results.worker_uid` is a foreign key.
- So register a `workers` row for each jobs node (region NULL, name
  `jobs:<host>`), or stop relying on `worker_uid`: filter evaluations out of
  `LastSignals` on `output.evaluation = true`, which every evaluation row
  already carries. Either way, an evaluation must never be read back as a
  signal. A test pins it.

Evaluation rows carry `region = NULL`: they did not run anywhere.

### 3. A new heartbeat is not down before it had a chance

`passiveEvaluation` reports Down "No heartbeat received" at the first evaluation
after creation. On 2026-09-24 that opened (and then resolved) incident #66 on a
freshly created host heartbeat whose sender was being deployed a few minutes
later.

- A check that has **never** received a signal stays `created` ("Waiting for
  the first signal") for its first 2 periods after `created_at`. After that it
  is Down as today, so a sender that is never set up still alerts.
- Once a first signal has arrived, the current rules apply unchanged.

### 4. Wording and docs

- The dashboard and docs say evaluations come from "the {{region}} checks
  worker":
  - `locales/*/checks.json:542-544`;
  - `evaluation-card.tsx:128-134`;
  - `web/docs/docs/features/check-types.md:1293-1298`.
  Change them to "Evaluated by SolidPing every period", in every locale.
- `check-types.md:1273-1276` documents a "Grace 30s" setting that does not
  exist. Remove it, or document the real rule: 1× period, 2× for a running
  signal.

### Tests

- A heartbeat created with `regions: ["gravelines"]` is stored with `[]` and
  gets exactly one NULL-region job.
- With every regional check worker stopped, an overdue heartbeat still goes
  Down within one period.
- An agent claim (org agent and system agent) never returns a passive job.
- Two jobs-node claimers produce one evaluation per period.
- Evaluation rows are never returned by `LastSignals`.
- Migration: existing heartbeats lose their regional jobs and are evaluated
  exactly once afterwards. The boot repair does not recreate regional jobs.
- Email checks follow the same path as heartbeats.
- A new heartbeat with no signal is `created` for 2 periods, then Down. One
  ping within that time makes it Up with no incident.

## Out of scope

- The private agent liveness monitor (spec `2026-09-25-05`). It builds on this
  evaluator.
- A grace setting for heartbeats (separate product decision).

## Implementation Plan

### §1 Passive checks have no regions

- `models.Check.IsPassive()` / `models.Check.JobRegions()` (nil for passive,
  `Regions` otherwise) and `checkerdef.PassiveCheckTypes()` (the one list, for
  SQL `IN (...)`), so every materialization point reads the same rule.
- Normalization (accept and drop, never reject):
  - `handlers/checks`: one helper `resolveRegionsForType(ctx, type, requested, org)`
    returning `[]` for passive types, used by the create plan (`plan.go`), the
    update plan (`plan.go`) and `UpdateCheck` (`service.go`), the
    config-as-code diff (`diff.go`, so a dropped list shows no change) and the
    rate-projection validator (`validate.go`). MCP create/update and
    config-as-code upsert all go through these service methods.
  - `db` `CreateCheck` (Postgres + SQLite) empties `Regions` for passive types,
    so raw-DB creators (samples, demo, test API) cannot store a region either.
- Job materialization skips regions for passive types: `createCheckJobs`
  (Postgres + SQLite) and `reconcileCheckJobs` use `check.JobRegions()`, so a
  passive check always owns exactly one NULL-region job.
- Boot repair: `ListChecksWithStaleJobRegions` (both engines) excludes passive
  checks from its two regional drift queries and adds a third: an enabled
  passive check that owns a regional job, or has no NULL-region job. The
  existing reconcile then heals it into one NULL-region job. It can never
  recreate regional jobs.
- Migration, new section `passive-checks-no-regions` appended to
  `024_v0_33_0` (both engines): `regions = '{}'` / `'[]'` on passive checks;
  delete regional passive jobs when a NULL-region job exists; keep one per
  check otherwise; convert the survivor to `region = NULL` with its lease
  cleared (kept rather than inserted so `scheduled_at` keeps the engine's own
  timestamp encoding). Boot repair covers anything left.
- dash0: `isPassiveCheckType` in `lib/check-scheduling.ts` becomes the one
  definition; `check-form.tsx` and the check detail page import it. The region
  picker (and region spread) is hidden for passive types and the form does not
  send `regions` for them.

### §2 One evaluation per period, on the jobs node

Shape: **one NULL-region `check_jobs` row per passive check + a jobs-node-only
claimer.** Not a jobs-table job: the jobs queue still wakes up to 5 minutes
late for future-scheduled work (spec `2026-09-25-07`), and a heartbeat with a
1-minute period needs a per-tick claimer. The claimer is a dedicated loop,
`checkworker.PassiveEvaluator`, started next to the job worker whenever the
node runs jobs (`ShouldRunJobs`).

- Claims: `checkjobsvc.ClaimPassiveJobs(workerUID, limit)` selects due
  (`scheduled_at <= now`), unleased, `region IS NULL`, passive-type rows,
  `FOR UPDATE SKIP LOCKED` on Postgres, and leases them with the normal
  lease writer (`scheduled_at + period + 30s`). It returns the same
  next-eligible hint the other claims do, so the loop sleeps until the next
  tick (capped at a 10s fallback poll).
- Evaluation: the existing `passiveEvaluation` via a shared core, written
  through `DirectBackend.SubmitResult` (save, `ProcessCheckResult`, release),
  so `checks.last_result_at` keeps moving like any real result.
  `Region = nil` on every evaluation row.
- Requirements: no region or check worker involved; a restart loses nothing
  (unfinished claims are released on shutdown, a crash only waits out the
  lease); two jobs nodes never evaluate one tick twice (row locks + leases).
- Exclusion: `applyCloudRegionScope` (cloud claim, express claim, hint) and
  `AgentScope.apply` (org and system agent claim, hint) both add
  `type NOT IN (passive)`. The CheckWorker's passive branch stays as a
  defensive path only; no production claim reaches it.

**Result marker: both.** Each jobs node registers a `workers` row (slug
`jobs-<node slug>`, name `jobs:<node name>`, region NULL). It is required
anyway: `check_jobs.lease_worker_uid` is a foreign key to `workers`, so the
lease needs an owner. Evaluation rows therefore carry a `worker_uid` and
`LastSignals`'s `worker_uid IS NULL` rule keeps working. In addition
`GetLastSignalForChecks` (both engines) now also excludes rows whose
`output.evaluation` is true, because `results.worker_uid` is
`ON DELETE SET NULL`: an evaluation whose worker row is deleted used to read
back as a signal. Tests pin both.

### §3 First-signal grace

In the shared evaluation core: when there is no signal on record and
`now < check.created_at + 2 × period`, nothing is written and the lease is
released to the next tick. The check keeps its `created` status and no
incident can open. After that window, or as soon as a signal exists, the
existing rules apply. Writing nothing (rather than a `created` row) keeps the
abandoned-result reaper from turning grace rows into "abandoned" 5 periods
later; the freshness threshold (3 × period) is wider than the grace.

### §4 Wording and docs

- `resultDetail.evaluation.explainer*` in all 4 locales: "Evaluated by
  SolidPing every period" wording, region-less; the two `*NoRegion` keys and
  the card's `regionLabel` prop are removed.
- `web/docs/docs/features/check-types.md`: drop the "Grace 30s" row and
  document the real rule (1× period, 2× for a running signal, 2 periods for a
  new check with no signal); the result-rows table says the evaluation is
  written by SolidPing, region none.

### Tests

1. Create with `regions: ["…"]` → stored `[]`, one NULL-region job (service
   test, create + update + config-as-code diff).
2. Evaluator with no check worker running: overdue heartbeat goes Down.
3. Agent claim (org + system scope) and cloud claim never return a passive
   job (checkjobsvc).
4. Two evaluators claiming the same due job → one evaluation (SQLite; plus a
   Postgres-layer claim test).
5. `LastSignals` never returns evaluation rows, including orphaned ones
   (worker deleted).
6. Migration section on a seeded pre-024 SQLite DB; boot repair heals a
   legacy passive check to one NULL job and does not recreate regional ones;
   the evaluator then evaluates it exactly once per tick.
7. Email checks through the same evaluator path.
8. New heartbeat: `created` for 2 periods (no row, no incident), then Down;
   a ping inside the window gives Up with no incident.
