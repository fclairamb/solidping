---
model: opus
effort: high
---

# Region health reports every private region as a ghost, and two liveness metrics are never set

## Problem

### Bug 1: `RegionHealth` ignores org agents

`checks.Service.RegionHealth` (`server/internal/handlers/checks/region_health.go:89`)
counts live workers only via `workers.region`. `workerCoverageForSlug`
(`region_health.go:286`) skips every row with `worker.Region == nil`.

Org agents never get a region on their worker row. `ensureWorkerRow`
(`server/internal/handlers/agentws/handler.go:439`) sets `row.Region` only when
`agent.IsSystem()`. The comment explains why: the `workers.region` check
constraint forbids the reserved `@` prefix, and agent claims never route through
the worker row (they go through `ClaimJobsForAgent` with a hard org + exact
region `AgentScope`, `handler.go:794-797`).

Consequences:

- Every private region (`@<slug>`) always reports `liveWorkers: 0`,
  `lastWorkerSeenAt: null`, and `ghost: true` as soon as a check references it,
  even while its agent is connected and claiming.
- Private region strings are **org-relative**: jobs and checks store `@<slug>`
  and the org is implicit in `organization_uid` (`regions.PrivateRegionSlug`,
  `server/internal/regions/regions.go:98`). `RegionHealth` keys everything by
  the bare string, so `@paris` in org A and `@paris` in org B are merged into one
  row: their check counts, job counts and overdue stats are summed.
- The platform watchdog dark-region detector
  (`server/internal/watchdog/detect_regions.go:21`) consumes this report, so it
  raises a dark-region anomaly for every private region in use. The anomaly
  `Subject` is `row.Slug` (`detect_regions.go:85`), which would also collide
  across orgs.
- `GET /api/v1/system/regions/health` returns the same wrong data.

This contradicts `specs/done/2026/08/2026-08-24-09-ghost-region-detection-api.md`
§5: "For private regions 'workers' are the org agents serving that scope."
The only private-region test (`region_health_test.go`, around lines 135-196)
covers a private region with no agent at all, so it passes by accident.

### Bug 2: dead liveness metrics

These gauges are registered (`server/internal/prommetrics/metrics.go`) with
setters in `server/internal/prommetrics/recording.go`, but no production code
calls the setters (grep for `prommetrics.<Setter>(` outside tests returns 0):

| Metric | Setter |
|---|---|
| `solidping_workers_active{region}` | `SetWorkersActive` |
| `solidping_check_up` | `SetCheckStatus` |
| `solidping_check_status_streak` | `SetCheckStatusStreak` |
| `solidping_checks_configured` | `SetChecksConfigured` |

The task that filed this named only the first two. The other two were found
while checking it.

`wiki/features/notifications-and-escalation.md:681-682` claims
`solidping_check_up`, `solidping_check_status_streak` (incident service) and
`solidping_workers_active` (heartbeat/claim paths) are populated. They are not.
Anyone alerting on `solidping_workers_active == 0` is alerting on a series that
never exists.

## Proposal

### 1. Derive private-region liveness from `agents`, scoped per org

Take the **`agents.last_seen_at` route**. Do not put the region on the org
agent's worker row: the `workers.region` check constraint forbids `@`, so that
route needs a migration on both Postgres and SQLite. The cloud claim path
(`checkjobsvc.applyCloudRegionScope`, `server/internal/checkworker/checkjobsvc/service.go:372-402`,
`region NOT LIKE '@%'` / `? LIKE region || '%'`) and `system.Service.LaneLoad`
also read `workers.region`, so that route would widen the blast radius for no
gain. `agents.last_seen_at` is already the agent liveness signal (refreshed on
pings and frames via `UpdateAgentLastSeen`, also used by `agentsupersede.go`).

In `RegionHealth`:

- **Key private rows by (org, slug).** Add an `organization` field (the org
  slug, `json:"organization,omitempty"`) to `RegionHealthRow`. It is empty for
  cloud regions and set for every `@` row. Cloud rows are unchanged.
- **Group the private-region inputs by org.** `regionCheckReferenceCounts` and
  `regionJobStatsBySlug` must group `@` slugs by `organization_uid` (cloud slugs
  stay global). Keep the "a handful of grouped queries, no per-check loop"
  performance rule from the 2026-08-24-09 spec.
- **Load agents once.** Select the non-deleted org agents
  (`organization_uid IS NOT NULL`) with `organization_uid`, `region`,
  `last_seen_at`. For a private row `(org, @slug)`:
  - `liveWorkers` = count of that org's agents with `region == @slug`,
    `last_seen_at >= now - regions.WorkerLivenessWindow`, and a status that
    can claim. Check `models.Agent.Status` values and exclude revoked/disabled
    agents.
  - `lastWorkerSeenAt` = max `last_seen_at` across that org's agents for the
    slug, including revoked ones. This matches the "includes soft-deleted
    workers" rule for cloud rows.
  - Match the region **exactly**, not by prefix. Agents claim by exact equality
    (`ClaimJobsForAgent`), and this report mirrors the claim predicate.
- **Cloud rows: keep the worker path.** System agents already record their
  region on the worker row. Do not also count them from `agents`, or they are
  counted twice.
- **Slug universe.** An org agent's region adds `(org, @slug)` to the universe,
  the same way a worker's announced region does today.
- `Ghost` stays `(jobs > 0 || checksReferencing > 0) && liveWorkers == 0`, now
  evaluated per (org, slug).

Fix the stale `models.Agent.Region` doc comment (`server/internal/db/models/agent.go`),
which still says org agents hold `@<org>/<region>`. They hold the org-relative
`@<slug>`, the same as jobs. Confirm this against the enroll path before editing.

### 2. Update the consumers

- **Watchdog** (`detect_regions.go`): sort and dedupe by (organization, slug).
  Make `Subject` unique per org, e.g. `<org>/@<slug>` for private rows, so two
  orgs' `@paris` never share an anomaly. Include the org in the headline and
  detail for private rows.
- **API**: add `organization` to the `RegionHealthRow` schema in
  `server/internal/app/openapi/openapi.yaml` and to
  `wiki/api-specification/` if the endpoint is documented there. Grep `web/dash0`
  for any consumer of `/system/regions/health` and update it if one exists.
- Update `wiki/` wherever the ghost-region endpoint or dark-region detector is
  described.

### 3. Metrics: wire `workers_active`, remove the per-check dead gauges

- **`solidping_workers_active{region}`: wire it** from the same computation, so
  it cannot disagree with `RegionHealth`. Set it in the watchdog's region pass
  (it already calls `RegionHealth` on a schedule), not as a side effect of the
  read-only HTTP handler. Call `WorkersActive.Reset()` before setting, so
  regions that vanish stop being exported. Emit **cloud regions only**:
  private slugs are org-relative, and a `region="@paris"` label would merge
  orgs again. If the watchdog can be disabled, say in the metric's help text
  that the gauge is populated by the watchdog, or pick another periodic
  owner. Do not leave it silently empty.
- **`solidping_check_up`, `solidping_check_status_streak`,
  `solidping_checks_configured`: remove** the collectors, their setters, their
  entries in the `MustRegister` list (`metrics.go:689-690`), and any tests that
  exist only to exercise them. Nothing reads them. The first two are per-check
  series (slug × type × region × org), which is unbounded cardinality on a SaaS
  install, and check state already lives in the DB and API. If one of them is
  wanted later, it should come back with a real write point in its own spec.
- Correct `wiki/features/notifications-and-escalation.md:681-682`. Drop the
  removed metrics, and say `solidping_workers_active` is set by the watchdog
  region pass from `RegionHealth`, cloud regions only. Grep `wiki/` and
  `web/docs/` for other mentions of the removed metric names.

### Tests

In `server/internal/handlers/checks/region_health_test.go` (SQLite, plus the
Postgres layer where the suite already runs there):

1. **Live org agent**: org A, check on `@x`, agent for org A with region `@x`,
   `last_seen_at` = now. Expect `liveWorkers: 1`, `lastWorkerSeenAt` set,
   `ghost: false`, `organization: <orgA slug>`.
2. **Stale org agent**: same, but `last_seen_at` =
   `now - WorkerLivenessWindow - 1m`. Expect `liveWorkers: 0`,
   `lastWorkerSeenAt` = that timestamp, `ghost: true`.
3. **Two orgs, same private slug**: org A and org B each have a check on `@x`,
   and only org A has a live agent. Expect two separate rows. A is not a ghost.
   B is a ghost with `liveWorkers: 0`. Each row's `checksReferencing` and
   `jobs` count only its own org.
4. **Cross-org negative with a positive control**: org B's live agent on `@x`
   must not make org A's `@x` live. Assert A is a ghost, then assert B is live
   in the same report.
5. **No double count**: a live system agent with a worker row on a cloud
   region counts exactly once.
6. **Revoked agent**: a live-timestamped but revoked org agent does not count.
7. Keep the existing "private region with no agent" test passing.

Watchdog (`server/internal/watchdog/`): two orgs with a dark `@x` produce two
anomalies with distinct `Subject`s. A private region whose org agent is live
produces no dark-region anomaly.

Metrics: after a watchdog region pass with one live cloud worker in `eu-1`,
`solidping_workers_active{region="eu-1"}` = 1. After that worker goes stale,
the gauge reads 0. No `@` label is ever emitted. Use
`prometheus/testutil.ToFloat64` / `CollectAndCount`.

### Out of scope

- Changing the org agent's worker row or the `workers.region` check constraint.
- Any change to `checkjobsvc` claim scoping. This spec only reads agent state.
- UI for per-org region health.

## Implementation Plan

### RegionHealth (`server/internal/handlers/checks/region_health.go`)

- Introduce an internal `regionKey{orgUID, slug}`. Cloud slugs always carry
  `orgUID == ""`; an `@` slug carries the owning row's `organization_uid`.
- `regionCheckReferenceCounts`: project `organization_uid, regions` off
  `checks` (still one query, `deleted_at IS NULL`) and count per `regionKey`.
- `regionJobStatsBySlug`: project `organization_uid, region, scheduled_at` off
  `check_jobs` (still one query, `region IS NOT NULL`) and aggregate per
  `regionKey`.
- New `orgAgentsForRegionHealth`: one query on `agents` with
  `organization_uid IS NOT NULL AND deleted_at IS NULL`, projecting
  `organization_uid, region, status, last_seen_at`. System agents are
  excluded by the `organization_uid` filter, so a system agent is only ever
  counted through its worker row (no double count).
- New `orgSlugsByUID`: one query on `organizations` (`uid, slug`, no
  `deleted_at` filter, `uid IN (...)` over the org UIDs that appear in private
  keys) so every private row can name its org. Falls back to the UID if the
  org row is missing.
- Universe: declared + check keys + job keys + non-deleted worker regions
  (cloud keys only; `@` worker regions are ignored, the constraint forbids
  them anyway) + every loaded org agent's `(org, @slug)`.
- Coverage: cloud keys keep `workerCoverageForSlug` (prefix rule). Private
  keys use new `agentCoverageForKey`: exact `region == slug` and same org;
  `liveWorkers` counts only `status == active` with
  `last_seen_at >= now - WorkerLivenessWindow`; `lastWorkerSeenAt` is the max
  `last_seen_at` over every loaded agent for the key, revoked included.
- `RegionHealthRow.Organization string json:"organization,omitempty"`: org
  slug on `@` rows, empty on cloud rows. Rows sorted by (slug, organization).
- Fix the `models.Agent.Region` / `AgentEnrollmentToken.Region` / `AgentKindOrg`
  doc comments: the enroll path (`agents.Service` mints the token with
  `regions.PrivateRegionSlug(req.RegionSlug)`, `EnrollAgent` copies
  `token.Region`) stores the org-relative `@<slug>`.

### Consumers

- Watchdog `detect_regions.go`: sort by (slug, organization); `Subject` is
  `<org>/@<slug>` for private rows (cloud unchanged); headline says
  `region "@x" of org "acme"`; detail carries `organization=<org>`;
  remediation for a dark private row points at the org's agents
  (`GET /api/v1/orgs/<org>/agents`) instead of the cloud-region migration.
- `Evaluate` keeps the RegionHealth report of the dark-region pass and
  stores `Report.CloudWorkersActive map[string]int` (cloud rows only, nil
  when the detector failed).
- openapi.yaml `RegionHealthRow.organization` + updated descriptions for
  `liveWorkers` / `lastWorkerSeenAt` / endpoint description; regenerate
  `server/pkg/client`. dash0 has no consumer (grepped). `wiki/api-specification`
  does not document the endpoint (grepped). Update
  `wiki/features/platform-watchdog.md`.

### Metrics

- Wire `solidping_workers_active{region}`: `watchdog.PublishMetrics` calls
  `prommetrics.WorkersActive.Reset()` then sets one series per cloud row
  when the dark-region detector succeeded (left untouched when it failed, same
  rule as the other watchdog gauges). Help text says it is populated by the
  platform watchdog region pass and is absent while the watchdog is disabled.
  `SetWorkersActive` replaced by `ResetWorkersActive`/`SetWorkersActive` pair
  as needed.
- Remove `solidping_check_up`, `solidping_check_status_streak`,
  `solidping_checks_configured`: collectors, setters, `allCollectors` entries,
  the now-unused `labelCheckSlug`, and their assertions in `metrics_test.go`.
- Fix `wiki/features/notifications-and-escalation.md` metrics table.

### Tests

- `region_health_test.go`: 7 cases from the spec (live, stale, two orgs, cross-org
  negative + positive control, system agent no double count, revoked, keep the
  no-agent test). Agents inserted directly (`models.NewAgent` /
  `models.NewSystemAgent`).
- `region_health_postgres_test.go`: a Postgres twin of the two-orgs case to
  cover the new `organization_uid` projections and the agents scan.
- Watchdog: two orgs with a dark `@x` → two anomalies, distinct subjects; a
  private region with a live org agent → no dark-region anomaly.
- Metrics (watchdog package, non-parallel because it mutates the global
  gauge): live `eu-1` worker → 1, stale → 0, no `@` label ever.
