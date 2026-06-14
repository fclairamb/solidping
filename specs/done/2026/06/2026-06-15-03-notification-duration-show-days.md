# Notification duration: show days when incident exceeds 24 hours

## Problem

All notification channels (Slack, Discord, ntfy, Pushover, Mattermost, Google Chat, OpsGenie) show incident duration as raw hours:

> ✅ Incident resolved after **239 hours 36 minutes**.

Users must mentally divide by 24 to understand this is ~10 days. The badges package already formats this correctly (`%dd`).

## Root cause

`formatDuration` in `server/internal/notifications/slack.go` (package-level, shared by all notifiers) has no `days` branch. It tops out at `"%d hours %d minutes"`.

## Fix

Add a days branch to `formatDuration`. When `hours >= 24`:

| Duration | Output |
|---|---|
| exactly 1 day, 0 hours | `1 day` |
| 1 day, 1 hour | `1 day 1 hour` |
| 1 day, N hours | `1 day N hours` |
| N days, 0 hours | `N days` |
| N days, 1 hour | `N days 1 hour` |
| N days, M hours | `N days M hours` |
| 239h 36m | `9 days 23 hours` |

Minutes are **dropped** at the days scale — they add noise at that granularity.

## Scope

- `server/internal/notifications/slack.go`: update `formatDuration`
- `server/internal/notifications/slack_test.go`: add test cases for day-scale durations

No other files change. All notifiers in the `notifications` package automatically benefit.

## Tests to add

```
10 days          → "10 days"
1 day            → "1 day"
1 day 1 hour     → "1 day 1 hour"
1 day 3 hours    → "1 day 3 hours"
9 days 23 hours  → "9 days 23 hours"  (was: "239 hours 36 minutes")
```

## Implementation Plan

1. `server/internal/notifications/slack.go` — extend the package-level `formatDuration`.
   Add a `days` branch that fires when `hours >= 24` (i.e. `int(dur.Hours()) >= 24`),
   computed as `days = hours / 24`, `remHours = hours % 24`. Minutes are dropped at this
   scale. Build the string with correct singular/plural for both units:
   - `days==1, remHours==0` → `1 day`
   - `days==1, remHours==1` → `1 day 1 hour`
   - `days==1, remHours>1`  → `1 day N hours`
   - `days>1,  remHours==0` → `N days`
   - `days>1,  remHours==1` → `N days 1 hour`
   - `days>1,  remHours>1`  → `N days M hours`
   All existing sub-24h behavior (seconds, minutes, hours+minutes) is preserved unchanged.
2. `server/internal/notifications/slack_test.go` — add a table-driven `TestFormatDuration`
   covering the day-scale rows from the spec plus singular/plural boundary cases, and a few
   sub-24h cases to guard against regressions. Use `testify/require`, `t.Parallel()`.
3. QA: `make fmt`, `make build-backend`, `make lint`, `make test`. All new cases must pass.
