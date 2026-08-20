---
sidebar_position: 17
title: SLOs & Uptime Reports
---

# SLOs & Uptime Reports

An **SLO** (service-level objective) turns "is it up?" into "is it up *enough*?".
You set a target — say 99.9% monthly — and SolidPing tracks how much of the
month's **error budget** you have left, how fast you are spending it, and when
it will run out at the current rate.

An **uptime report** is the same information mailed on a schedule, so the people
who never open a dashboard still see it.

## How attainment is measured

Attainment is the **probe ratio**: successful checks divided by countable checks
over the window. It is exactly the number your status pages and badges already
show, so an objective can never contradict them.

A few consequences worth knowing:

- A `warning` result counts as **up** — the target is reachable, there is just
  something to report. This matches every other availability surface.
- A window with **no results at all** reports *no data*, not 100%. "We were not
  watching" is not the same statement as "everything was fine", and rendering it
  as a perfect month would be the most dangerous thing this page could do.
- Confirmed incidents are shown alongside as context ("3 incidents, longest 42
  min"). They are never the attainment number.

## Windows

Windows are **calendar months in the objective's own timezone**. That matches
how contracts are written ("99.9% monthly"), and it means a month containing a
daylight-saving transition is honestly an hour shorter or longer — the budget
follows the wall clock, not a nominal 30 × 24h.

Nothing is stored per window. Attainment, budget and history are recomputed from
the monthly rollups every time you look, and those rollups are kept forever — so
an objective you create today already knows about last year.

## Error budget

| Field | Meaning |
|---|---|
| **Budget** | `(1 − target) × monitored time` over the whole window. A 99.9% monthly target on a 30-day month buys about 43 minutes. |
| **Consumed** | The failure share applied to the time that has already elapsed. |
| **Remaining** | Budget minus consumed. Negative when you have overspent. |
| **Burn rate** | Observed error rate ÷ allowed error rate. `1.0` spends the budget exactly over the window; `2.0` spends it in half the window. |
| **Projected exhaustion** | When the remaining budget runs out at the current burn rate. Blank when the budget is not being spent, is already spent, or would survive the window. |

The status chip summarises it:

| Chip | Meaning |
|---|---|
| **Healthy** | Budget remains and is being spent no faster than the target allows. |
| **At risk** | Budget remains, but the burn rate is above 1 — at this pace you will breach before the window closes. |
| **Breached** | The window's budget is spent. |
| **No data** | The window carries no countable probe. |

## Burn-down

The objective's detail page plots the **error-budget burn-down** for the current
window: how much budget is left, day by day, against the straight line that
would spend it exactly by the end of the window. Above the line you are fine;
below it you are on the burn rate that reaches zero early.

The actual line is not clamped at zero — once the budget is overspent it keeps
going, because how far past the line you are is the thing worth seeing. It also
never goes back up: budget spent is spent, and a quiet stretch flattens the line
rather than refunding it. A day with no results at all spends nothing, for the
same reason a window with no results has no attainment.

## Scope

An objective covers **exactly one check or exactly one check group** — never
both, never neither. A group objective means the group's *current* members:
group membership is not versioned, so a check that left the group last week is
simply absent from today's evaluation of last month.

Group math is a weighted average — successes and totals are summed across
members, never percentages averaged — so a one-member group and that member's
own objective always agree.

## Excluding planned maintenance

By default an objective **subtracts probes recorded during an active maintenance
window** from both sides of the ratio. Without this, a two-hour planned upgrade
costs a 99.9% monthly budget roughly 4.6× its entire allowance, which makes the
number useless for any team that does planned work.

Two things to know:

- The tag is written **when the result is recorded**, so it is not retroactive.
  Months that predate the feature simply have nothing to exclude, and the status
  API reports `excludedMaintenanceSeconds` explicitly so a partially-covered
  month is legible rather than quietly wrong.
- It affects **only the objective**. Status pages, badges and the availability
  API keep showing raw availability exactly as before.

See [Maintenance Windows](maintenance-windows.md) for how windows are defined.

## Uptime reports

A report schedule mails a digest **weekly or monthly**, in its own timezone,
covering either the whole organization or a chosen set of checks and groups.
Each digest carries overall availability, a per-check breakdown, the incident
summary, and — when enabled — the error-budget state of every objective in
scope.

- **Send me a test** renders the report for the period that most recently closed
  and mails it to *you*. It never fans out to the recipient list.
- Every message carries a `List-Unsubscribe` header. Unsubscribing removes the
  address from the schedule itself, not just from that one send — so nobody can
  be re-subscribed by an operator simply re-saving the schedule.

## API

Full endpoint reference: `GET /api/v1/orgs/:org/slos`, `/slos/:uid/status`,
`/slos/:uid/history`, and `/report-schedules`. The generated API reference
documents every request and response schema under the **SLOs** tag.
