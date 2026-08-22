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

The series is **cumulative**, and consumption is accrued **per step and summed
forward** — not derived from the window-to-date failure ratio times the
window-to-date wall clock. That distinction is load-bearing: the ratio moves when
probe *density* changes (a group member created mid-window, a check paused after
an outage), so the ratio form can compute less consumption at a later point and
the burn-down climbs back up. Per-step accrual can only ever add, so
`budgetRemainingSeconds` is monotonically non-increasing by construction.

A step with **no countable probe spends nothing** — no data is "we were not
watching", not downtime — so a coverage gap flattens the line rather than
dropping it.

The value is deliberately **not clamped at zero**: an overspent budget reports a
negative remainder, because the magnitude of a breach is the thing the chart
exists to show.

`budgetTotalSeconds` is the window's whole allowance, evaluated once.
`idealRemainingSeconds` is that same number decayed linearly to zero at the
window end — the pace that spends the budget exactly, no faster. Both series are
drawn against the one budget, so they stay comparable even as excluded
maintenance shrinks the monitored denominator.

Steps are fixed 24h slices from the window start rather than local calendar
days; the window itself stays calendar-exact, only the sampling grid is uniform.

### GET /api/v1/orgs/:org/slos/:uid/history?months=12
Past calendar windows, most recent first, computed off the permanent month
rollups. `months` defaults to 12 and is capped at 60. Auth: required.
Response: `{ "data": [ <same shape as `current`> ] }`.

## Burn-rate alert policies

Spec: `2026-08-21-08`. Google-SRE-style **multiwindow, multi-burn-rate**
alerting on top of the burn rate the status endpoint already reports.

- Two **built-in** policies per objective, materialized on demand (an objective
  created before the feature existed answers exactly like a new one):

  | `kind` | Long window | Short window | Default threshold | Severity |
  |---|---|---|---|---|
  | `fast` | 1h | 5m | 14.4x | `critical` |
  | `slow` | 6h | 30m | 6x | `warning` |

- Both start **disabled**. Alerting is opt-in — upgrading must never start
  paging on its own.
- An alert fires only when **both** windows exceed the threshold: the long
  window proves the burn is significant, the short one proves it is still
  happening. That is what stops a spike that ended forty minutes ago from
  paging for the rest of the hour.
- Thresholds and windows are **stored, not derived**, so operators can tune
  them. `kind` is not writable.
- A window carrying fewer than `minSamples` countable probes is
  **inconclusive**: it does not fire, and it does not count as "below
  threshold" for the auto-resolve hysteresis either. Sparse data must not
  fabricate an alert, nor silently close one.
- `results.maintenance` is excluded exactly as the objective's own denominator
  excludes it, so planned maintenance never pages.
- Firing opens an **incident** with `kind: "slo_burn"`, bound to the SLO and
  the policy, through the ordinary incidents service — so ack, snooze, manual
  resolve, escalation policies and severity-gated channel routing all apply
  unchanged. At most one open burn incident exists per objective+policy
  (enforced by a partial unique index); while it is open the evaluator updates
  it, tracking the peak burn rate, rather than paging again.
- Auto-resolve is **hysteretic**: both windows must sit below the threshold for
  a full short-window duration. Resolving an alert by hand while the objective
  is still burning opens a fresh incident on the next evaluation, the same rule
  check incidents follow.
- Burn incidents never publish to a status page: a burn rate is an internal
  operations signal about an error budget, not a customer-facing outage.
- Evaluation runs once a minute (`slo_burn_eval` job, not publicly creatable).

`GET /api/v1/orgs/:org/slos` and `/slos/:uid` carry `burning: true` while any
policy on the objective has an open burn incident.

### GET /api/v1/orgs/:org/slos/:uid/alert-policies
Both policies with their live state. Auth: required.

```json
{
  "data": [
    {
      "uid": "...", "sloUid": "...", "kind": "fast", "enabled": true,
      "longWindowSeconds": 3600, "shortWindowSeconds": 300,
      "threshold": 14.4, "severity": "critical", "minSamples": 3,
      "lastEvaluatedAt": "2026-08-21T09:14:00Z",
      "longBurnRate": 31.2, "shortBurnRate": 44.5,
      "longSamples": 60, "shortSamples": 5,
      "longConclusive": true, "shortConclusive": true,
      "overThresholdNow": true,
      "firing": true, "incidentUid": "...", "incidentNumber": 42,
      "firingSince": "2026-08-21T08:57:00Z", "resolvingSince": null
    }
  ]
}
```

`longBurnRate` / `shortBurnRate` are recomputed for the request rather than read
back from the stored readout, so the number on screen is current even when the
evaluator is a minute behind. `null` means the window carries no countable
probe — never `0`, which would read as "healthy".

`resolvingSince` is the hysteresis anchor: when set, both windows have been
below threshold since that instant and the incident auto-resolves once that has
held for a full short window.

### GET /api/v1/orgs/:org/slos/:uid/alert-policies/:policyUid
One policy, same shape. Auth: required. A policy belonging to another objective
is a `404`, not an edit.

### PATCH /api/v1/orgs/:org/slos/:uid/alert-policies/:policyUid
Tune a policy. Auth: required.

Body (all optional): `enabled`, `longWindowSeconds`, `shortWindowSeconds`,
`threshold`, `severity` (`critical` | `warning`), `minSamples`.

`shortWindowSeconds` must be > 0 and ≤ `longWindowSeconds`, and the long window
is capped at 7 days — beyond that the short window stops being answerable from
the raw-retention band and the alert stops being about "right now". A violation
is a `400`, not a `500` from the CHECK constraint.

Changing a threshold or either window **clears the hysteresis anchor**: it was
measured against the old numbers, and carrying it forward could auto-resolve an
incident on evidence gathered under a rule that no longer applies.

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
