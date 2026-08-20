# SLOs and Uptime Reports

Service-level objectives (error budgets over calendar windows) and the
scheduled uptime-report digests. Spec: `2026-08-20-01`.

## Concepts

- **Source of truth** is probe-ratio availability (`successful/total` from the
  shared `uptimebar` engine) — the same numbers badges and status pages show,
  so an SLO page can never contradict a status page. The incident wall-clock
  view is shown as context, never as the attainment number.
- **Windows** are calendar months in the SLO's own IANA timezone. Month rollups
  are permanent, so full history is answerable with no extra storage.
- **Nothing is stored per window.** Attainment, budget and history are computed
  at read time. The emailed report is the only frozen artifact.
- **Attainment is nullable.** A window with no countable probe reports
  `attainmentPct: null` and an untouched budget — never 100%. Same rule as the
  availability API.
- **Maintenance exclusion is not retroactive.** `results.maintenance` is tagged
  at ingest, so months that predate the feature simply have nothing to exclude.
  `excludedMaintenanceSeconds` is always reported so a partially-covered month
  is legible.

## SLOs

### GET /api/v1/orgs/:org/slos
List the organization's objectives. Auth: required.
Query: `checkUid` (restrict to objectives scoped directly to that check),
`limit` (1-200, default 100).

### POST /api/v1/orgs/:org/slos
Create an objective. Auth: required. Gated by the `maxSlos` entitlement — a
breach returns `402` with `code: QUOTA_EXCEEDED`.

Body: `name`, `slug` (optional, derived from the name), exactly one of
`checkUid` / `checkGroupUid`, `targetPct` (0 < x ≤ 100, default 99.9),
`timezone` (IANA, default `UTC`), `excludeMaintenance` (default `true`),
`enabled` (default `true`).

Sending both scope fields, or neither, is a `400`. The schema carries the same
rule as a CHECK constraint, so it cannot be bypassed by any other writer.

### GET /api/v1/orgs/:org/slos/:uid
Get one objective, by UID or slug. Auth: required.

### PATCH /api/v1/orgs/:org/slos/:uid
Update an objective. Auth: required. A scope change must name exactly one side;
the other column is cleared in the same write.

### DELETE /api/v1/orgs/:org/slos/:uid
Soft-delete an objective. Auth: required. Results are untouched.

### GET /api/v1/orgs/:org/slos/:uid/status
Evaluate the current calendar window. Auth: required.

```json
{
  "slo": { "uid": "...", "name": "API availability", "targetPct": 99.9, "...": "..." },
  "current": {
    "window": { "start": "2026-08-01T00:00:00Z", "end": "2026-09-01T00:00:00Z", "label": "2026-08" },
    "attainmentPct": 99.94,
    "hasData": true,
    "targetPct": 99.9,
    "totalChecks": 40320,
    "successfulChecks": 40296,
    "monitoredSeconds": 2678400,
    "elapsedSeconds": 1728000,
    "budgetTotalSeconds": 2678,
    "budgetConsumedSeconds": 1036,
    "budgetRemainingSeconds": 1642,
    "excludedMaintenanceSeconds": 0,
    "burnRate": 0.6,
    "projectedExhaustionAt": null,
    "state": "healthy",
    "partial": true
  },
  "incidents": { "count": 3, "longestSeconds": 2520, "averageSeconds": 900, "totalDowntimeSeconds": 2700 }
}
```

Field notes:

- `monitoredSeconds` is the **whole** window's monitorable duration (clamped by
  the check's creation and by observed maintenance). It is the budget basis:
  "99.9% monthly" allows 0.1% of the month, not 0.1% of however much of the
  month has elapsed.
- `elapsedSeconds` is the part of that which has already happened; it is the
  consumption basis.
- `burnRate` is observed error rate ÷ allowed error rate. `1.0` spends the
  budget exactly over the window, `2.0` spends it in half the window. `null`
  when there is no data, or when `targetPct` is 100 (no allowance to divide by).
- `projectedExhaustionAt` is `null` when the budget is not being consumed, is
  already spent, or would survive past the end of the window.
- `state` is `healthy` | `at_risk` | `breached` | `unknown`. `unknown` is the
  no-data state and must never render as healthy.

### GET /api/v1/orgs/:org/slos/:uid/burndown
Error-budget burn-down for the current window. Auth: required.
Response: `{ window, targetPct, budgetTotalSeconds, data: [ { at,
budgetRemainingSeconds, idealRemainingSeconds, attainmentPct, hasData } ] }`.

The series is **cumulative**: every point re-evaluates the whole window from its
start up to that instant, so `budgetRemainingSeconds` is monotonically
non-increasing. It is deliberately not clamped at zero — an overspent budget
reports a negative remainder, because the magnitude of a breach is the thing the
chart exists to show. `idealRemainingSeconds` is the straight line from the full
budget at the window start to zero at its end: the pace that spends the budget
exactly, no faster.

Steps are fixed 24h slices from the window start rather than local calendar
days; the window itself stays calendar-exact, only the sampling grid is uniform.

### GET /api/v1/orgs/:org/slos/:uid/history?months=12
Past calendar windows, most recent first, computed off the permanent month
rollups. `months` defaults to 12 and is capped at 60. Auth: required.
Response: `{ "data": [ <same shape as `current`> ] }`.

## Report schedules

Recipients are PII and get the same handling bar as status-page subscribers:
returned only to the org's own authenticated admins, never logged, never
emitted into events.

### GET /api/v1/orgs/:org/report-schedules
List the organization's schedules. Auth: required.

### POST /api/v1/orgs/:org/report-schedules
Create a schedule. Auth: required.
Body: `name`, `frequency` (`weekly` | `monthly`, default `monthly`),
`timezone` (IANA, default `UTC`), `recipients` (array of addresses, max 50),
`checkUids` / `checkGroupUids` (both empty = org-wide), `includeSlos`
(default `true`), `enabled` (default `true`).

### GET /api/v1/orgs/:org/report-schedules/:uid
Get one schedule. Auth: required.

### PATCH /api/v1/orgs/:org/report-schedules/:uid
Update a schedule. Auth: required.

### DELETE /api/v1/orgs/:org/report-schedules/:uid
Soft-delete a schedule. Auth: required.

### POST /api/v1/orgs/:org/report-schedules/:uid/test
Render the report for the period that most recently closed and mail it to the
**caller's own address** (or to `recipient` in the body). Auth: required.
Returns `202`. It deliberately does not fan out to the schedule's recipients:
"preview this" must never be a button that mails the whole distribution list.
The suppression list is honored here too.

## Delivery

The `uptime_report` job sweeps hourly, looking for schedules whose weekly or
monthly period has closed in their own timezone. Multi-replica safety comes
from a conditional `last_period_start` claim, so two replicas noticing the same
closed period send exactly one report. It is **not** publicly creatable.

Every email carries `List-Unsubscribe`, and unsubscribing org-wide removes the
address from every report schedule in the organization — not merely from the
delivery path, so an operator re-saving the schedule cannot silently re-enable
mail to someone who asked to stop.
