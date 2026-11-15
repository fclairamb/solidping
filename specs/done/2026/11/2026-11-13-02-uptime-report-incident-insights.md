---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# Add response times and most-failing checks to the scheduled uptime report

## Problem

The scheduled uptime report (`server/internal/uptimereport/report.go`) gives the incident count and the total downtime (`report.go:159-165`), and sorts its check table by availability (`report.go:520`). It does not say how fast incidents were acknowledged or resolved, nor which checks opened the most incidents. A check that flaps ten times for 30 seconds each keeps a good availability and stays at the bottom of the table.

Spec `2026-11-13-01` adds these numbers to the dashboard. The report should carry the same ones, computed by the same code.

Depends on `2026-11-13-01-incident-insights-page` (package `server/internal/incidentinsights`).

## Proposal

1. In `Builder.measure` (`report.go:334`), call `incidentinsights.Compute` on the incidents of the report window, restricted to the checks in scope (`scopeChecks`, `report.go:602`). Reuse the incidents the builder already loads for `incidentBlock` (`report.go:659`) if it has them, no second query.
2. Extend `Data` (`report.go:140`) with:
   - `MedianTimeToResolve` and `MedianTimeToAcknowledge`, formatted like `TotalDowntime`
   - `AcknowledgedCount` (shown as "12 of 40 acknowledged")
   - `TopFailingChecks`: up to 5 rows with name, link (`checkReportURL`, `report.go:791`), incident count and relapse count, ordered by incident count
3. Render them in the email (`write.go` and the report template):
   - one line under the existing incident figures: median time to resolve, median time to acknowledge, acknowledged count
   - a "Most incidents" block listing the 5 rows
4. Hide rules:
   - no incident in the period: neither the line nor the block is rendered
   - no acknowledged incident: the acknowledge figure is omitted, the acknowledged count still shows "0 of N"
   - a single check in scope: the "Most incidents" block is omitted (the table already says it)
5. Add the strings to every report locale, and the new fields to the preview fixtures (`server/internal/handlers/emailpreview/fixtures_report.go`).
6. Keep the email inside the size budget checked by `size_test.go`.
7. `CHANGELOG.md` entry, and a line in the report docs page of `web/docs/docs/`.

## Tests

- `server/internal/uptimereport/builder_test.go`:
  - a window with acknowledged and resolved incidents fills the three figures and orders `TopFailingChecks` by incident count
  - a window with no incident leaves them empty
  - an incident on a check outside the schedule's scope is not counted
- `server/internal/uptimereport/render_test.go`:
  - the line and the block are rendered when there are incidents
  - neither is rendered with zero incidents (positive control: the same test renders them with one incident)
  - the acknowledge figure is absent and "0 of N acknowledged" present when nothing was acknowledged
  - the block is absent with a single check in scope
- `locale_report_test.go`: the new strings exist in every locale.
- `size_test.go`: still passes with 5 top rows and the longest locale.

## To verify

- Whether `incidentBlock` (`report.go:659`) keeps the incident slice or only the aggregate. If only the aggregate, have it return the slice.
- The template file the report is rendered from, and the locale files it reads.
