---
model: opus
effort: high
---

# Renaming a region strands its check_jobs forever — add a server-scope region migration API

## Problem

On 2026-08-24 the solidping.k8xp.com deployment renamed its worker regions
(`default` → `gravelines`, `eu-2` → `paris`, `us-1` → `kansas-city`) by
changing `SP_NODE_REGION` on the check workers and re-seeding `SP_REGIONS`
([regions_seed.go:30](server/internal/app/regions_seed.go#L30)). The result was
a **silent 3-hour outage of 84% of the fleet**: 526 of 627 enabled checks
stopped executing, 419 `check_jobs` rows sat overdue, and 61 incidents froze in
`active` state (including ~50 whose underlying targets had recovered hours
earlier).

The mechanism:

- A job row carries the region it was materialized for
  ([check_job.go:16](server/internal/db/models/check_job.go#L16), `Region *string`).
- The claim predicate matches the **worker's** region against the **job's**
  region by prefix:
  `WhereOr("? LIKE region || '%'", *region)`
  ([service.go:372](server/internal/checkworker/checkjobsvc/service.go#L372)).
  `'gravelines' LIKE 'default%'` is false, so a job written under the old slug
  is unclaimable by every worker registered under the new one — forever.
- Nothing rewrites existing rows. `reconcileCheckJobs` aligns jobs to
  `checks.regions` but only runs when a check is **edited**
  ([service.go:1849](server/internal/handlers/checks/service.go#L1849)), and the
  startup pass `ReconcileStaleJobSchedules` detects stale **periods** only, not
  stale regions
  ([service.go:2613](server/internal/handlers/checks/service.go#L2613)).
- Restarting the server does nothing (verified live): the breakage is persisted
  state, not scheduler memory.

The live remediation was 311 individual `PATCH /orgs/stonal/checks/:uid` calls
re-sending each check's own `regions` array just to trigger the reconcile —
and that path was only available because the *checks* already carried the new
slugs. It also could not touch the other orgs' checks (org-scoped auth), so
~125 jobs in `public`/`portrait`/`webingenia`/`demo` stayed stranded. Region
renaming is a deployment-level operation; recovering from it must not require
per-org admin credentials and O(checks) API calls.

Note that `checks.regions` can *also* hold the old slug (the live incident had
checks already updated; a plain rename-without-check-edit leaves the old slug
in both places), so the fix must cover both tables.

## Proposal

Add a **super-admin, server-scope migration endpoint** that reassigns every
reference to a region slug in one call:

```
POST /api/v1/system/regions/migrate
{ "from": "default", "to": "gravelines", "dryRun": true }
```

Registered on the existing super-admin `/system` actions group
([server.go:1463-1465](server/internal/app/server.go#L1463)), same
`RequireAuth` + `RequireSuperAdmin` middleware chain.

Semantics:

1. **Validation.** `from` and `to` are required, distinct, non-empty slugs.
   `to` must be a *declared* region — present in the `regions` system parameter
   (`regions.ParamRegions`) or served by a live non-deleted worker
   ([worker.go:19](server/internal/db/models/worker.go#L19), `last_active_at`
   within the liveness window). Migrating *to* a slug nobody serves would just
   move the stranding; reject it with a 422 naming the known regions. `from`
   deliberately need **not** be declared — the whole point is cleaning up slugs
   that no longer exist. Private-region prefixes (`@…`) are allowed on both
   sides but `from='@x'`→`to='y'` (private → cloud) must be rejected: sealed
   configs ([check_job.go:25](server/internal/db/models/check_job.go#L25)) are
   encrypted to a region key and cannot be re-targeted server-side.
2. **Scope of the rewrite.** In one transaction:
   - `checks.regions`: replace `from` with `to` in every array that contains
     it (dedupe if `to` already present).
   - `check_jobs.region`: for each affected check, run the existing
     `reconcileCheckJobs` against the updated check rather than a raw
     `UPDATE … SET region` — reconcile already handles the unique
     `(check_uid, region)` index, phase re-leveling, plan-weight refresh and
     job deletion/creation, and a hand-rolled UPDATE would collide when a
     `to`-region job already exists for the same check.
   - Cross-org by design: this is the server operator's surface, org boundaries
     do not apply (precedent: the fleet-wide system agents view, spec
     2026-08-05-01).
3. **Dry run.** `dryRun: true` returns the full report (below) without writing.
   The dashboard/CLI flow is: dry-run, show the blast radius, confirm, apply.
4. **Report.** Response for both modes:
   ```json
   {
     "from": "default", "to": "gravelines", "dryRun": false,
     "checksUpdated": 370, "jobsReassigned": 370, "jobsDeleted": 0,
     "byOrg": {"stonal": 311, "public": 38, "...": 0},
     "overdueRecovered": 345
   }
   ```
   `overdueRecovered` = jobs whose `scheduled_at` was in the past at migration
   time — the number the operator actually cares about.
5. **Idempotent.** A second call with the same pair finds zero references and
   returns zeros; not an error.
6. **Audit.** Log at WARN with actor UID, pair, and counts — this is a
   fleet-wide mutation and must be traceable to a person.

### Close the root cause too: startup reconcile must catch stale regions

`ReconcileStaleJobSchedules` exists precisely to heal drift at boot but only
compares periods. Add the symmetric query — enabled checks having at least one
job whose `region` is not in the check's current `regions` array (and checks
missing a job for a region they declare) — and feed those checks through the
same `reconcileCheckJobs` loop
([service.go:2622-2637](server/internal/handlers/checks/service.go#L2622)).
With that in place, tonight's class of incident self-heals on the next deploy
even if the operator forgets the migration call. Keep it idempotent and
logged with a reconciled-count, matching the existing startup pass.

### Tests

- Migration moves `checks.regions` + jobs, respects the unique index when the
  target job already exists, refuses undeclared `to`, refuses private→cloud,
  is idempotent, and honors `dryRun` (no writes — assert on row counts).
- Startup pass: seed a job under a slug absent from its check's `regions`,
  boot, assert the job got reconciled without any API call.
- Auth: org admin gets 403; only super-admin passes.

Out of scope: renaming the region *definition* itself in the `regions`
parameter (that is already editable via `/system/parameters`), and any
automatic detection/alerting of stranded jobs — that is spec
`2026-08-24-09` (detection) and `2026-08-24-10` (watchdog).
