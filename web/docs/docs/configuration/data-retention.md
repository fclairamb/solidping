---
sidebar_position: 7
title: Data Retention
---

# Data Retention

SolidPing does not keep every check result forever. Results are progressively
**rolled up** into coarser summaries by a background aggregation job, and the
finer-grained rows are deleted once they have been summarized. This page
explains what is kept, for how long, and how to tune it.

## How results age

Every check execution is first stored as a **raw** result — the exact status,
response time, and diagnostic output of that single run. As results get older
they are rolled up through three summary tiers:

| Tier | One row covers | What it contains |
|------|----------------|------------------|
| `raw` | a single check execution | status, response time, full diagnostic output |
| `hour` | one hour | success/total counts, min/avg/max/p95 response time |
| `day` | one day | same summary fields, per day |
| `month` | one month | same summary fields, per month — **kept forever** |

Each rollup **deletes the rows it summarized**: once an hour of raw results
becomes an hourly summary, the individual executions (including their
diagnostic output) are gone. Availability numbers are unaffected — summaries
carry exact success/total counts — but per-execution detail only exists in the
raw tier.

Monthly summaries are never deleted, so long-term availability (a 365-day or
year-to-date window) always has data to draw on, no matter how the retention
settings below are tuned.

### What this means in the dashboard

- **Response-time charts** show individual data points only within the raw
  window; older ranges show hourly/daily averages.
- **Status-page and badge uptime bars** need hour or day rows for their
  per-hour/per-day ticks. Ticks older than the day-tier horizon show as
  "no data" — raising the day retention extends how far back daily bars reach.
- **Availability percentages** (check detail, `365d`, `ytd`) read all tiers,
  including monthly summaries, so they stay accurate beyond the horizons above.

## Defaults

| Setting | Default | Meaning |
|---------|---------|---------|
| Raw retention | `24` hours | Per-execution detail for about a day |
| Hourly retention | `7` days | Hourly summaries for about a week |
| Daily retention | `2` months | Daily summaries for about two months |

A value of `N` keeps the current in-progress period plus `N − 1` completed
ones; older periods are rolled up on the aggregation job's next pass.

## Configuration

Each tier can be set two ways. An environment variable takes precedence over
the dashboard setting — if the env var is set, edits in the UI have no effect.

**Environment variables:**

```bash
SP_PERFORMANCE_AGGREGATION_RETENTION_RAW_HOURS=24
SP_PERFORMANCE_AGGREGATION_RETENTION_HOUR_DAYS=7
SP_PERFORMANCE_AGGREGATION_RETENTION_DAY_MONTHS=2
```

**Dashboard:** as a server administrator, open **Server → Aggregation** and
edit the three values there (stored as the system parameters
`performance.aggregation_retention_raw_hours`, `…_hour_days`,
`…_day_months`).

Values must be **whole numbers ≥ 1**; anything else is rejected. Changes
apply on the aggregation job's next pass — no restart needed.

## Other retained data

Monitoring results are not the only thing SolidPing keeps on a clock.

| Data | Default retention | Setting |
|---|---|---|
| Audit events | 365 days | `SP_AUDIT_RETENTION_DAYS` / `audit.retention_days` |
| Support-inbox threads and messages (closed only) | 365 days | `SP_SUPPORT_RETENTION_DAYS` / `support.retention_days` |

The [support inbox](../features/support-inbox.md) is the one that stores
**personal data** — free text written by identifiable people, arriving from
publicly reachable phone numbers. Only **closed** threads are purged, and the
sweep takes their messages with them. Setting the value to `0` keeps everything,
which is the switch to use under a legal hold.

If you publish a privacy policy for your instance, its retention table needs a
row for this store: capture without the matching policy text puts what you
publish in conflict with what your service does.

## Raise retention before you need it

:::caution Rollups are irreversible
Raising a retention value does **not** restore history that has already been
rolled up — the finer-grained rows were deleted when they were summarized.
If you want 90 days of daily bars on your status page in Q3, the day
retention must already be raised in Q2.
:::

Conversely, **lowering** a value makes more data eligible for rollup, so
granular history shrinks to the new horizon within a few aggregation passes.

## Choosing values

Retention is the main lever on database size: raw rows dominate storage (a
1-minute check writes 1,440 rows per day per region), hourly rows cost 24 per
day, daily rows 1 per day, monthly rows are negligible.

- **Debugging-heavy setups** (you often inspect individual failures): raise
  raw retention to `48`–`72` hours.
- **Status pages with 90-day bars**: raise daily retention to `4` months or
  more so every tick has data.
- **Small databases / high check volume**: keep the defaults, or lower raw
  retention — summaries preserve availability accuracy at a fraction of the
  storage.
