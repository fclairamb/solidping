---
model: opus
effort: high
---

# Every public status-page view scans the whole `results` table to keep 15 % of what it reads

## Problem

[`fetchRecentResults`](server/internal/handlers/statuspages/service.go#L2307)
loads the response-time points for every resource on a status page with one
query that has **no time bound and no period-type filter**:

```go
recentFilter := &models.ListResultsFilter{
    OrganizationUID: orgUID,
    CheckUIDs:       checkUIDs,
    Limit:           responseTimeLimit * regionFanoutCap * len(checkUIDs), // 100 * 20 * N
    SkipBlobs:       true,
}
```

Three compounding problems, in order of cost:

**1. No `PeriodTypes` → no index is eligible.** As in spec `2026-08-22-04`,
every useful index on `results` is partial on `period_type = 'raw'` /
`!= 'raw'`. A query that constrains neither can use none of them. The plan is a
sequential scan of the entire table.

**2. No `PeriodStartAfter`.** The query is unbounded in time, so the scan
cannot shrink even in principle.

**3. The row budget is global, and 40 000 rows wide.** For a 20-check page the
limit is `100 × 20 × 20 = 40 000` rows, ordered globally by `period_start DESC`
— which forces an external merge sort to disk. Go then keeps at most 100 per
`(check, region)`: **~6 000 of the 40 000 rows are used, 85 % are discarded.**
Because the budget is global rather than per-check, it is simultaneously
over-fetching *and* — for a page mixing fast and slow checks — capable of
starving the slow ones.

### Measured

Same synthetic dataset as spec `2026-08-22-04` (3.0 M rows, 2.1 GB, production
indexes), 20-check page:

| shape | plan | time |
|---|---|---|
| **current** | Parallel Seq Scan + external merge sort to disk | **662 ms** |
| + 24 h time bound only | Parallel Seq Scan | 109 ms |
| `LATERAL` per check, `period_type='raw'`, 300 each | Index Scan `results_raw_idx` | **2.9 ms** |
| `LATERAL` per check, raw + non-raw branches | Index Scans, both partial indexes | **40 ms** |

Every visitor pays the 662 ms — this is the public, unauthenticated page, and
[it is not cached](server/internal/handlers/statuspages/handler.go#L567) (spec
`2026-08-22-06`).

### The trap: `LATERAL` alone makes it 18× worse

Rewriting to a per-check `LATERAL` **while keeping the "any period type"
semantics** produces a *per-check* sequential scan — twenty of them:

| shape | plan | time |
|---|---|---|
| `LATERAL` per check, **no** `period_type` | Seq Scan on `results`, ×20 | **12 274 ms** |

The `period_type` predicate is not an optimisation here, it is what makes the
rewrite work at all. An implementation that adds the `LATERAL` and leaves the
tier open takes the page from 0.66 s to 12 s. This is the single most important
constraint in this spec.

### Why "any period type" exists at all

The budget is 100 points per `(check, region)`. For a 1-minute check that is
100 minutes — comfortably inside the 24 h raw retention, so raw alone suffices.
For a 1-hour check it is 100 hours, which is **past** raw retention, so those
points only exist as `hour` rollups. The current code gets both by filtering
neither. The replacement has to cover both tiers explicitly.

## Proposal

Fetch per `(check, tier)` with an explicit tier predicate, and bound each tier
to the window where that tier's data actually lives.

### 1. A dedicated DB method, not another `ListResults` flavour

`ListResults` is a generic filtered list; expressing "top N per check per tier"
through it means either N+1 round-trips or the current over-fetch. Add a
purpose-built method to `db.Service`:

```go
RecentResultsPerCheck(ctx, orgUID string, checkUIDs []string,
    tiers []string, perCheckLimit int, since time.Time) ([]*models.Result, error)
```

implemented in **both dialects** as a `LATERAL` (Postgres) / correlated
subquery (SQLite) over the check-UID list, one branch per tier, `UNION ALL`ed:

```sql
select l.* from unnest($check_uids::uuid[]) c(uid), lateral (
    (select … from results r
      where r.organization_uid = $org and r.check_uid = c.uid
        and r.period_type  = 'raw' and r.period_start >= $raw_since
      order by r.period_start desc limit $per_check)
  union all
    (select … from results r
      where r.organization_uid = $org and r.check_uid = c.uid
        and r.period_type <> 'raw' and r.period_start >= $rollup_since
      order by r.period_start desc limit $per_check)
) l;
```

Both branches carry a tier predicate, so both hit a partial index. Neither
branch may be written without one.

### 2. Size the per-check budget from regions, not from a global fan-out cap

`perCheckLimit` becomes `responseTimeLimit × (regions actually observed for
that check)`, falling back to `regionFanoutCap` only when the region set is
unknown. The existing global `100 × 20 × len(checkUIDs)` product disappears —
it was compensating for the lack of per-check isolation, which `LATERAL`
now provides directly.

### 3. Bound each tier by its own retention

`raw_since` is `now - rawRetention`, resolved through
[`systemconfig.ResolveAggregationRetention`](server/internal/systemconfig/retention.go#L53)
— the documented single source of truth, already used for exactly this purpose
by `uptimebar`'s raw clamp. Do **not** read `cfg.Aggregation.RetentionRaw`
directly: the server Aggregation settings tab writes `performance.*` DB
parameters that never reach the koanf struct, and a reader that clamps to 24 h
while the job actually keeps 168 h silently drops six days of raw rows that no
rollup covers yet.

`rollup_since` is the window the response-time chart can display —
`responseTimeLimit` buckets back at the coarsest tier in play, with slack.

The org's `uptimebarHints` ([`service.go:462`](server/internal/handlers/statuspages/service.go#L462))
already resolves retention for this service; reuse it rather than adding a
second resolution path.

### 4. Merge preferring the finer tier

Group by `(check, region)`, sort by `period_start DESC`, and take the newest
`responseTimeLimit` points — raw first where it exists, rollups filling the
older tail. `buildResponseTimeSeries` and `buildResponseTimeData` are unchanged;
they already receive a per-region slice and already read `duration_p95` for
aggregated rows.

### Testing

- **Plan assertions (Postgres, testcontainers).** `EXPLAIN` the generated query
  and assert **no** `Seq Scan on results` — for a 1-check page and for a
  20-check page. Include an explicit regression case asserting that a tier-less
  variant is *not* what gets generated; the 12 s failure mode is invisible to
  every functional test.
- **Point-parity against today.** For a seeded page mixing a 1-minute check and
  a 1-hour check, the series returned must match what the current
  implementation returns: same regions, same point counts, same ordering. The
  1-hour check is the case that fails if the rollup branch is dropped — it must
  be in the fixture, not assumed.
- **Per-check isolation.** A page with one very dense check and one sparse
  check must return the full budget for **both**. Under the current global
  limit the sparse check can be starved; this test pins that it no longer can.
- **Retention resolution.** With `performance.aggregation_retention_raw_hours`
  set to a non-default value in the DB, the raw branch's `since` follows it —
  proving the resolution goes through `systemconfig` and not the koanf field.
- **SQLite parity.** The same assertions on point counts and ordering against
  the SQLite implementation (`make test` covers both dialects).
- **Group resources still get no series.** `resourceRecentResults` deliberately
  returns nil for a group or a multi-member resource — per-member latency is the
  thing a group exists to hide. Pin it; the rewrite touches its input.

### Acceptance

- A 20-check status page issues no sequential scan on `results`.
- Rows fetched drop from ~40 000 to ~6 000 for that page, with no series losing
  points.
- Both dialects produce identical series for the same fixture.

## Out of scope

- Caching the page response — spec `2026-08-22-06`. The two are independent:
  caching without this fix just makes the 662 ms rarer, and this fix without
  caching still runs a query per visitor.
- `uptimebar.BucketAvailability`, the other half of `enrichWithAvailability`.
  It already splits by tier, time-bounds, caps rows and sets `SkipBlobs`, and
  measures 15 ms on the same dataset. It is the pattern to copy, not to change.

## Implementation Plan

### 1. `models.RecentResultsPerCheckFilter` (new, `internal/db/models/result.go`)

The spec sketches `RecentResultsPerCheck(ctx, orgUID, checkUIDs, tiers, perCheckLimit, since)`.
Two things in the same spec make that literal signature unable to express what
§2 and §3 ask for, so the parameters move into a filter struct — the shape
`ListResults` already uses in this repo:

- §2 wants a **per-check** budget (`responseTimeLimit × regions of that check`),
  not one scalar for the whole batch.
- §3 wants a **per-tier** `since` (`raw_since` ≠ `rollup_since`) in the single
  `UNION ALL` statement the spec's SQL shows.

```go
type RecentResultsTier struct {
    PeriodTypes []string  // must sit entirely on ONE side of the raw/rollup split
    Since       time.Time // this tier's own lower bound
}

type RecentResultsPerCheckFilter struct {
    OrganizationUID      string
    CheckUIDs            []string
    Tiers                []RecentResultsTier
    PerCheckLimits       map[string]int
    DefaultPerCheckLimit int
}
```

`Validate()` **rejects** a tier whose `PeriodTypes` is empty or
`models.PeriodTierMixed`. That is the structural guard for the spec's single
most important constraint: the type system offers no way to ask for "any period
type", so the 12 s per-check-seq-scan failure mode cannot be written.

`models.ResultColumnsWithoutBlobs()` returns the non-blob column list both
dialects project (the method is response-time-chart-only, so blobs are never
read), guarded by a reflection test against the model's bun tags.

### 2. `db.Service.RecentResultsPerCheck` — both dialects

- **Postgres** (`internal/db/postgres/postgres.go`): `unnest(?::uuid[], ?::int[])
  AS cu(uid, lim)` + `CROSS JOIN LATERAL`, one `UNION ALL` branch per tier, each
  branch carrying `period_type = 'raw'` / `period_type != 'raw'` **plus** the
  `IN (…)` list, `ORDER BY period_start DESC LIMIT cu.lim`. Mirrors the existing
  `GetLastResultForChecks` shape.
- **SQLite** (`internal/db/sqlite/sqlite.go`): no `unnest`, no `LATERAL` — a
  `UNION ALL` of one bounded subquery per `(check, tier)`, each with the same
  restated tier predicate (SQLite does not derive the partial-index predicate
  from an `IN` list — spec 2026-08-22-04).

### 3. `uptimebar.RawTierStart` exported

The raw branch's `since` is the same clamp uptimebar already applies
(`now - (RetentionRaw + 2 h margin)`), resolved through
`systemconfig.ResolveReadSideRetention` via the service's existing
`uptimebarHints`. Exporting the existing unexported `rawTierStart` keeps one
implementation instead of a second resolution path.

### 4. `fetchRecentResults` rewritten (`internal/handlers/statuspages/service.go`)

- Takes the `uptimebar.Hints` both call sites already compute.
- Per-check budget: `GetChecksByUIDs` once, then
  `responseTimeLimit × (len(check.Regions)+1)` — the `+1` covers the NULL/legacy
  region bucket — capped at `responseTimeLimit × regionFanoutCap`, and falling
  back to that cap when the check (and so its region set) is unknown.
- Two tiers: `raw` from `RawTierStart(...)`, and `hour,day,month` from
  `now - responseTimeRollupSpan`.
- Merge: group by `(check, region)`, sort `period_start DESC` preferring raw on
  a tie, truncate to `responseTimeLimit`. `buildResponseTimeSeries` /
  `buildResponseTimeData` unchanged.

### 5. Tests

| Spec test | Where |
|---|---|
| Plan assertions + tier-less positive control | `internal/db/postgres/status_page_recent_results_plan_postgres_test.go` |
| SQLite plan (index name, no `SCAN results`) + control | `internal/db/sqlite/recent_results_per_check_plan_test.go` |
| Point parity vs. the pre-fix global-limit fetch, 1-min + 1-hour checks | `internal/handlers/statuspages/recent_results_test.go` |
| Per-check isolation (dense check cannot starve a sparse one) | same |
| Retention resolution via `performance.*` DB parameter | same |
| Group / multi-member resources still get no series | same |
| Mixed-tier filter rejected | `internal/db/models` + both dialects |

### Out of scope (siblings)

No `Cache-Control` work (spec 06) and no change to the dash0 chart's fetch seam
or `systemconfig/retention.go` (spec 07).
