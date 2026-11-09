---
model: opus
effort: medium
pr: own
automerge: false
---

# Dashboard availability window: 24h, 7 days or 30 days

## Problem

The dashboard's availability tile, and the all-green banner under it, only ever show the trailing 24 hours. A user who
was away for a week (or wants the monthly view) has no fleet-wide figure for that period: the per-check availability
table has 24h and 30d but no 7d row, and nothing aggregates across checks beyond 24h.

## Proposal

1. `GET /api/v1/orgs/{org}/checks/stats` gains `availability7d` and `availability30d` next to `availability24h`. Same
   fold, same `null` rule (no countable data is `null`, never 100).
   - `db.Service.GetOrgAvailability24h` becomes `GetOrgAvailability(ctx, orgUID, now, windows)`. It still runs exactly
     two queries, one per partial index (`raw`, and every rollup tier), with one conditional `SUM` per window.
   - The rollup query now reads `period_type <> 'raw'` instead of `= 'hour'`. 7d and 30d windows reach `day` rows
     (hour retention is 7 days by default). A rollup row counts when its `period_start` is inside the window, the edge
     rule of `uptimebar.WindowAvailability`. For 24h this changes nothing: no day row starts inside a trailing day.
2. `users.preferences` gains `dashboardWindow` (`24h` | `7d` | `30d`, empty means 24h), set through
   `PATCH /auth/me {preferences: {dashboardWindow}}`. Any other value is `400 VALIDATION_ERROR`. The column is JSON, so
   no migration.
3. dash0: a `24h / 7 days / 30 days` segmented control in the dashboard header. It switches the availability tile and
   the banner (figure, label, SLA pill) at once, since all three values come in the same stats response, and saves the
   choice to the user's preferences. Demo and impersonating sessions keep the choice for the page load only.
4. The per-check availability table gets a "Last 7 days" row.

## Out of scope

- The "Checks at a glance" uptime strips stay hourly over 24h. A 7d or 30d strip needs a server-side org-wide bucket
  endpoint, since the hourly results query cannot carry 168 or 720 buckets per check.
- A URL parameter for the window, and a configurable (add/remove tiles) dashboard.

## Testing

- `availability24h_test.go` and its Postgres twin: every case asserts the three windows; new cases for hour rows older
  than 24h (7d only), a day row older than 7d (30d only) and rows older than 30d (none).
- `preferences_test.go`: set, clear and refuse `dashboardWindow`.
- `e2e/dashboard.spec.ts`: the selector switches tile and banner and sends the PATCH.
