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
