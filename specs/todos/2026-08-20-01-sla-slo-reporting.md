---
model: opus
effort: high
---

# SLA/SLO reporting: no SLO concept, no error budgets, no scheduled uptime reports

## Problem

SolidPing aggregates results per period with min/max/avg/p95 and computes
timezone-aware availability, but there is no way to express "this check must be
99.9% up this month", see how much error budget remains, or get an uptime
report emailed at the end of the month. The roadmap (`wiki/roadmap.md` §2.3)
calls this out: the data exists, the concept doesn't. The buyer-facing effect
is real — uptime reports reach the manager who approves the invoice, and the
competitor inventory shows only Site24x7 ships genuine SLA reporting
out of the box (`wiki/competitors/site24x7.md`), while nobody in the space
ships error budgets.

Concretely missing today:

- **No SLO object.** The availability API
  (`GET /orgs/:org/checks/:check/availability`,
  `server/internal/handlers/availability/service.go`) is single-check,
  read-only, and explicitly deferred "SLA target / error-budget fields"
  (`specs/done/2026/06/2026-06-30-10-check-availability-server-side-statistics-api.md`,
  Out of scope).
- **No group or org-level availability.** `uptimebar.WindowAvailability`
  (`server/internal/uptimebar/window.go:49`) already batches multiple check
  UIDs, but no endpoint exposes an aggregate above a single check.
- **Maintenance windows never excluded from availability.** They only suppress
  incident creation (`server/internal/handlers/incidents/service.go:151-206`,
  `isInMaintenanceWindow`). A planned 2h maintenance costs a 99.9% monthly
  budget ~4.6× its entire allowance, which makes any SLO number untrustworthy
  for teams that do planned work.
- **No scheduled reports.** The email templates directory
  (`server/internal/email/templates/`) has no report/digest template; no job
  emits periodic summaries.

## Proposal

Build the SLO layer on the existing engines: `uptimebar` for math, the
self-rescheduling job queue for scheduling, `TemplateFormatter` + `JobTypeEmail`
for delivery, entitlements for gating.

### Definitions (decisions, not options)

- **Source of truth: probe-ratio availability** — `successful/total` from
  `uptimebar`, the same numbers badges and status pages already display, so the
  SLO page can never contradict the status page. The incident wall-clock view
  (already computed side-by-side in `availability/service.go`) is shown as
  context in reports ("3 incidents, longest 42 min"), never as the attainment
  number.
- **Windows: calendar months in a per-SLO IANA timezone.** Matches contract
  language ("99.9% monthly"), and `month` rollups are permanent
  (`server/internal/systemconfig/retention.go` — never rolled further, never
  deleted), so full history is answerable with zero new storage. Rolling
  windows are out of scope for now.
- **Nothing stored per window.** Attainment, budget, and history are computed
  at read time from rollups. The emailed report is the frozen artifact.

### Data model

Two new tables (Postgres + SQLite, per `sync-pg-to-sqlite` parity):

**`slos`** — `uid`, `organization_uid`, `name`, `slug`, exactly one of
`check_uid` / `check_group_uid` (CHECK constraint XOR, FK `on delete cascade`),
`target_pct numeric` (0 < target ≤ 100, e.g. 99.9), `timezone` (IANA, default
`UTC`), `exclude_maintenance boolean default true`, `enabled`, timestamps,
soft delete. Group SLOs mean "current members" — groups are 1:n
(`checks.check_group_uid`), membership changes are not versioned; the spec
accepts and documents this semantic.

**`report_schedules`** — `uid`, `organization_uid`, `name`, `frequency`
(`weekly` | `monthly`), `timezone`, `recipients` (JSONB array of emails —
PII, same handling bar as status-page subscribers), scope (org-wide, or JSONB
lists of group/check UIDs), `include_slos boolean`, `enabled`, timestamps.
Kept separate from `slos`: digests are useful for checks with no formal
objective.

### Maintenance exclusion — ingest-time tagging (ships first, it's non-retroactive)

Rollup buckets cannot be sliced after the fact, so tag at ingest:

1. Add `maintenance boolean not null default false` to `results`. Set it when
   the result is recorded while an active maintenance window covers the check —
   reuse the window-resolution + 60s cache logic from
   `incidents/service.go:151`, lifted into a shared service both callers use.
2. Aggregation (`server/internal/jobs/jobtypes/job_aggregation.go`) carries two
   new counters up the tiers on aggregated rows: `maintenance_checks`,
   `maintenance_successful_checks` (subset of total/successful, so existing
   consumers are unchanged).
3. `uptimebar.BucketStats` gains the excluded counts; the SLO read path
   subtracts them when `exclude_maintenance` is set. Status pages and badges
   keep showing raw availability — no behavior change outside SLOs.

Tagging only accrues from deploy day; the SLO status API reports
`excludedMaintenanceSeconds` explicitly so partial-coverage months are legible.

### API (conventions: `data` envelope, `$uid` paths, camelCase)

- `GET/POST /api/v1/orgs/:org/slos`, `GET/PATCH/DELETE /api/v1/orgs/:org/slos/:uid`
- `GET /api/v1/orgs/:org/slos/:uid/status` — current window: `targetPct`,
  `attainmentPct` (nullable when no data, matching the availability API's
  no-data ≠ 100% rule), `monitoredSeconds`, `budgetTotalSeconds`
  (`(1 − target) × monitoredSeconds`), `budgetConsumedSeconds`,
  `budgetRemainingSeconds`, `excludedMaintenanceSeconds`, `burnRate`
  (consumption rate ÷ sustainable rate over the window so far),
  `projectedExhaustionAt` (nullable), plus the incident context block reusing
  `PeriodIncidents` from the availability API.
- `GET /api/v1/orgs/:org/slos/:uid/history?months=12` — past monthly windows
  off the permanent month rollups.
- `GET/POST /api/v1/orgs/:org/report-schedules` + `GET/PATCH/DELETE /:uid`,
  and `POST /:uid/test` to send the report immediately to the caller.

Group SLO math follows the `mergeGroupBuckets` precedent
(`server/internal/handlers/statuspages/service.go:1872`).

### Scheduled reports

New self-rescheduling job `JobTypeUptimeReport` following the
`job_agent_gc.go` pattern: `JobType` const in `jobdef/types.go`, runner in
`jobtypes/`, factory in `registry.go`, seeded per-org in `job_startup.go`, not
added to `publiclyCreatableJobTypes`. Multi-replica safety comes free from
`SELECT … FOR UPDATE SKIP LOCKED` claiming + duplicate suppression in
`jobsvc` — no leader election.

Each run: find schedules whose period closed in their timezone (weekly → week
start, monthly → 1st), compute availability + SLO attainment for the scope,
render a new `uptime-report.html` via `TemplateFormatter`
(`server/internal/email/formatter.go`, `base.html` layout, premailer), and
enqueue one `JobTypeEmail` per recipient. `List-Unsubscribe` headers and the
suppression list (`handlers/emailsuppressions/`) are already wired into the
sender and must be respected; unsubscribing a recipient removes them from the
schedule.

### Dashboard (dash0)

Per the design-reference-first rule and house conventions (dedicated routes,
no modals):

- `/orgs/$org/slos` — list: name, scope, target, attainment, budget-remaining
  bar, status chip (healthy / at-risk / breached).
- `/orgs/$org/slos/new`, `/orgs/$org/slos/$uid` — detail with budget burn-down
  chart for the current window and the monthly history table.
- Report schedules under org settings, same route pattern.
- Check detail page shows an SLO chip when the check is covered by one.

### Entitlements

`maxSlos *int` following the seven-step recipe
(`server/internal/db/models/entitlements_payload.go:37` field + strict
`UnmarshalJSON` shadow, `entitlements/service.go` merge branch,
`defaults.go` per-mode defaults — SaaS 2, self-hosted nil —
`usage.go` `SloCreateAllowed` returning `QuotaError`/402, usage counter, wiki
table update). Report schedules are not separately gated for now.

### Phasing

1. **Phase 1**: migrations (both tables + `results.maintenance` + aggregation
   counters), ingest tagging, SLO CRUD + status/history endpoints, dashboard
   pages, entitlement.
2. **Phase 2**: report schedules CRUD + `JobTypeUptimeReport` + email template
   + test-send endpoint.

Both phases belong to this spec. **Out of scope** (future specs): burn-rate
*alerting* through the escalation pipeline (multiwindow fast/slow burn — the
`burnRate` field in the status API is deliberately its foundation), rolling
windows, per-region SLOs, public SLA pages on status pages
(`specs/ideas/2026-06-10-status-page-seo.md`), CSV/PDF attachments.

### Testing

- Table-driven tests for budget math (nullable attainment, partial windows via
  `monitoredSeconds`, maintenance subtraction, group merge) and for window
  resolution across timezones/DST.
- Aggregation tests proving maintenance counters survive raw→hour→day→month
  and never double-count (tiers are disjoint).
- A negative control: an SLO over a check with zero results must report null
  attainment and untouched budget, not 100%.
- Report job tests: period-close detection in tz, duplicate-run suppression,
  suppression-list respect.
- Playwright E2E for the SLO list/detail happy path.

### Open questions

- Should `warning` results consume budget? `uptimebar` counts warning as up
  (`accumulateRaw`); proposal: keep that for v1, consistency with every other
  surface beats configurability.
- SaaS default for `maxSlos` (2 proposed) needs syncing with
  `solidping-billing`'s Free SKU before release.
