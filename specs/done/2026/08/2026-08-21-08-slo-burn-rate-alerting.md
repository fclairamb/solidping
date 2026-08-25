---
model: opus
effort: xhigh
---

# SLO burn rates are computed but never alert — wire multiwindow burn-rate alerts into the incident pipeline

## Problem

SLO/SLA reporting shipped (spec `2026-08-20-01`) with `BurnRate` deliberately
laid as the foundation for alerting:
[budget.go:77](server/internal/slo/budget.go:77) defines it (observed error
rate ÷ allowed error rate; 1.0 spends the budget exactly by period end),
[budget.go:172](server/internal/slo/budget.go:172) computes it, and the
projected-exhaustion readout classifies on it. But nothing *consumes* it: an
org that burns a month of error budget in an hour finds out from the dashboard
or the monthly digest, not from a page. Burn-rate alerting is the piece that
turns the SLO feature from reporting into operations, and it was explicitly
left open on the roadmap.

## Proposal

Google-SRE-style **multiwindow, multi-burn-rate** alerts, reusing the existing
incident/escalation machinery end to end rather than a parallel alerting path.

### Alert policies

Per SLO, an optional alerting config with two built-in policies (both
individually toggleable, sane defaults, thresholds editable):

| Policy | Long window | Short window (confirm) | Default threshold | Severity |
|---|---|---|---|---|
| Fast burn | 1h | 5m | 14.4× | critical |
| Slow burn | 6h | 30m | 6× | warning |

An alert fires only when **both** windows exceed the threshold — the long
window proves significance, the short window proves it is still happening
(this is what prevents alerting on a long-resolved spike). Defaults follow the
SRE-workbook 99.9% table; thresholds are stored per policy, not derived, so
operators can tune them.

### Evaluation

- A periodic evaluator (every minute-ish; follow the scheduled-job pattern of
  [reportschedules/service.go](server/internal/handlers/reportschedules/service.go))
  computes windowed error rates at 5m/30m/1h/6h per alert-enabled SLO, from
  the same result data the burndown uses
  ([slo/window.go](server/internal/slo/window.go),
  [handlers/slos/burndown.go](server/internal/handlers/slos/burndown.go)).
  Reuse rollups where granularity allows; short windows may need raw-results
  queries — keep them indexed and bounded.
- Sparse data must not fabricate alerts: require a minimum sample count per
  window (below it, the window is inconclusive and does not fire).
- Maintenance-tagged results (`results.maintenance`) are already excluded
  from the objective's denominator — the evaluator must apply the same
  exclusion, or planned maintenance would page.
- Group-scoped SLOs aggregate exactly the way the budget does — one
  evaluator code path shared with `BudgetFor`, not a reimplementation.

### Alert lifecycle — an incident, not a new object

- Firing opens an **incident bound to the SLO** (new incident kind, e.g.
  `slo_burn`, carrying `sloUid` + the policy that fired), created through the
  existing incidents service so ack/snooze/manual-resolve, escalation
  policies, severity-gated channel routing, and group correlation all apply
  unchanged. Notification templates get a burn-specific message (current burn
  rate, budget remaining, projected exhaustion).
- **Dedup**: at most one open burn incident per SLO+policy; while open, the
  evaluator updates it (burn peak) rather than opening more.
- **Auto-resolve with hysteresis**: resolve only when *both* windows sit
  below threshold for a full short-window duration — no flapping. Manual
  resolve while still burning re-opens on the next evaluation (same rule as
  check incidents reopening).
- Audit events for fire/resolve alongside the existing `incident.*` types.

### Surface

- API: alert-policy CRUD nested under the existing SLO endpoints; extend
  [wiki/api-specification/slos.md](wiki/api-specification/slos.md).
- dash0: SLO detail gains an "Alerting" section — policy toggles, thresholds,
  current windowed burn rates, live fire state. SLO list shows a burning
  badge. Design-reference components throughout.
- Config-as-code: policies export/import with the SLO.

### Explicitly out of scope

Rolling-window objectives, per-region objectives, public SLA sections on
status pages, and CSV/PDF report attachments stay open as separate roadmap
items — burn evaluation over short windows is independent of the
calendar-month budget window, so none of them block this.

## Implementation Plan

### 0. Design decisions taken up front

**Burn incidents are ordinary incidents with a new `kind`.** `incidents` gains
`kind text not null default 'check'` (`check` | `slo_burn`) plus the binding
columns `slo_uid` and `slo_alert_policy_uid`. Everything downstream of
`incidents.emitEvent` — notification fan-out, escalation policies,
severity-gated channel routing, ack/snooze/manual-resolve, the incidents list,
`CancelPendingForIncident` — therefore works unchanged. No parallel alerting
path exists.

**Routing anchor.** `incidents.check_uid` is `NOT NULL` with an FK to `checks`,
so a burn incident carries a *representative check*: the SLO's own check for a
check-scoped SLO, and the lowest-sorted live member for a group-scoped SLO.
`check_group_uid` stays NULL on purpose — setting it would (a) collide with the
existing `uq_active_group_incident` partial unique index and (b) route through
`queueGroupNotifications`, which deliberately skips escalation policies. The
representative check is what resolves channels *and* the escalation policy, so
burn alerts get the full paging machinery for both scope kinds.

**Kind must be invisible to the check state machine.** Three DB lookups drive
`ProcessCheckResult` and dependency rollup off "is there an open incident on
this check": `FindActiveIncidentByCheckUID`, `FindRecentlyResolvedIncidentByCheckUID`
and `FindActiveIncidentsForChecksInWindow`. All three gain `kind = 'check'`, or
a burn incident would be mistaken for the check being down. `ListIncidentsFilter`
gains `Kinds []string` (empty = all, so the dashboard list still shows burn
incidents) and the availability / SLO / uptime-report incident blocks pass
`Kinds: ['check']` so a burn alert never counts as downtime against the very
objective that produced it.

**No status-page publication.** `publishOpened` / `publishResolved` are skipped
for burn incidents: a burn rate is an internal operations signal, not a
customer-facing outage.

**Severity.** `incidents` has no severity column in this codebase — severity is
a per-org channel-set attached to *escalation policy steps*. The policy's
`severity` (`critical` / `warning`) is therefore stored on the policy row and
carried into the incident title, `details` and every notification body; channel
gating continues to come from the escalation policy, unchanged.

### 1. Schema — `SECTION: slo-burn-alerts` in `015_v0_18_0` (both dialects)

New table `slo_alert_policies`:

| Column | Notes |
|---|---|
| `uid`, `organization_uid`, `slo_uid` | FK to `slos(uid) on delete cascade` |
| `kind` | `fast` \| `slow` — the built-in identity, unique per SLO |
| `enabled` | default **false**: alerting is opt-in, upgrading must not start paging |
| `long_window_seconds`, `short_window_seconds` | stored, not derived |
| `threshold` | burn-rate multiple, stored per policy so operators can tune |
| `severity` | `critical` \| `warning` |
| `min_samples` | default 3; below it a window is inconclusive |
| `last_evaluated_at`, `last_long_burn_rate`, `last_short_burn_rate` | live readout |
| `below_threshold_since` | hysteresis anchor for auto-resolve |

`incidents` gains `kind`, `slo_uid`, `slo_alert_policy_uid` and a partial unique
index `uq_active_slo_burn_incident (slo_uid, slo_alert_policy_uid) where state = 1
and kind = 'slo_burn' and deleted_at is null` — the dedup rule enforced by the
database, not merely by the evaluator.

Defaults, per the SRE workbook 99.9% table: fast = 1h/5m/14.4x/critical,
slow = 6h/30m/6x/warning.

### 2. Models + db layer
- `server/internal/db/models/slo_alert_policy.go` — model, `NewSLOAlertPolicy`,
  `DefaultSLOAlertPolicies`, `SLOAlertPolicyUpdate`.
- `models.Incident` gains `Kind`, `SLOUID`, `SLOAlertPolicyUID`;
  `IncidentKindCheck` / `IncidentKindSLOBurn` constants.
- `db.Service` gains policy CRUD, `ListEnabledSLOAlertPolicies` (all orgs, joined
  to live enabled SLOs — the evaluator's work queue), `FindActiveBurnIncident`
  and `ListActiveBurnIncidentsForSLOs` (the list badge, one query).
- Postgres + SQLite implementations kept in lockstep.

### 3. Shared evaluation path
`slos.Service.EvaluateWindows(ctx, orgUID, row, windows, now)` — resolves the
SLO's scope **once** and runs the existing private `evaluate` per window. That is
literally the same code path `GetStatus` / `GetHistory` / `EvaluateWindow` use, so
group aggregation, coverage clamping and `results.maintenance` exclusion cannot
drift from the objective's own denominator.

### 4. Evaluator — `server/internal/handlers/sloalerts/`
`Service.Evaluate(ctx, now)`:
1. `ListEnabledSLOAlertPolicies`.
2. Per policy, evaluate `[now-long, now)` and `[now-short, now)` via
   `EvaluateWindows` (distinct windows computed once per SLO).
3. A window is **conclusive** only when it has data and `totalChecks >= min_samples`.
4. **Fires** when both windows are conclusive and both burn rates exceed the
   threshold. Opens exactly one incident per SLO+policy (`FindActiveBurnIncident`
   first, unique index as the backstop).
5. While open, `UpdateSLOBurnIncident` refreshes `details` and tracks `burn_peak`.
6. **Auto-resolve with hysteresis**: `below_threshold_since` is stamped the first
   time both windows sit below threshold and cleared whenever either goes back
   above; resolve only once that has held for a full short-window duration.
7. Manual resolve while still burning: the next evaluation finds no open burn
   incident and opens a fresh one — the same semantic check incidents have.

Import-cycle note: `jobtypes` cannot import `handlers/incidents`, so
`app/services.Registry` gains a `SLOBurn SLOBurnEvaluator` interface field
(declared in `app/services`, implemented by `sloalerts`), and the periodic job
only calls through it.

### 5. Incident entry points — `handlers/incidents`
New exported, non-HTTP methods reusing `emitEvent` end to end:
`OpenSLOBurnIncident`, `UpdateSLOBurnIncident`, `AutoResolveSLOBurnIncident`.
Audit events are the existing `incident.created` / `incident.resolved` types,
with the burn payload attached, so the timeline, ack links and resolution
notices all work with no new event plumbing.

### 6. Periodic job
`jobdef.JobTypeSLOBurnEval` + `jobtypes/job_slo_burn_eval.go`, self-rescheduling
every minute, seeded by `job_startup.go` — the same shape as the uptime-report
and snooze-sweep jobs.

### 7. Notifications
`Payload` carries the burn numbers; the email view model gains an `SLOBurn`
block rendered by `incident-created.html` / `incident-resolved.html`, and Slack's
created/resolved builders gain the same three lines: **current burn rate, budget
remaining, projected exhaustion**. Every other sender degrades to the existing
generic body plus the burn-specific incident title.

### 8. API
- `GET /api/v1/orgs/:org/slos/:uid/alert-policies` — both policies with their
  live windowed burn rates, conclusiveness, and current fire state.
- `GET|PATCH /api/v1/orgs/:org/slos/:uid/alert-policies/:policyUid`.
- SLO list/get responses gain `burning`.
- `wiki/api-specification/slos.md` + `openapi.yaml` extended.

### 9. dash0
- SLO detail gains an **Alerting** card: per-policy enable toggle, threshold and
  window readout, live long/short burn rates, and a live firing badge linking to
  the open incident.
- Threshold/window editing lives on the existing dedicated edit route
  (`/orgs/$org/slos/$uid/edit`), per the "editing always navigates to a route"
  convention.
- SLO list gains a **Burning** badge.
- Design-reference primitives only (`Card`, `StatTile`, `Switch`, `Badge`,
  `Table`); responsive at every breakpoint.

### 10. Config-as-code — N/A, reported explicitly
The spec asks for policies to "export/import with the SLO". There is **no SLO
export/import path in this repository**: config-as-code is checks-only
(`handlers/checks/export_v2.go`), a scope decision documented in spec
`2026-08-19-08`. Building an SLO document format is a separate feature, so this
bullet is left unimplemented and called out rather than silently skipped.

### 11. Tests
- `slo` / `sloalerts` unit tests: threshold crossing, both-windows rule,
  min-sample inconclusiveness, dedup, hysteresis, manual-resolve reopen,
  maintenance exclusion, group aggregation.
- SQLite migration replay test for the new section.
- dash0 Playwright: Alerting card on SLO detail, list badge.
