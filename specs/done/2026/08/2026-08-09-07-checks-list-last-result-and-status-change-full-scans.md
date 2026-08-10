---
model: opus
effort: high
---

# The checks list burns a CPU core on two full scans of `results` per request — and one of them returns the retention horizon instead of a status change

## Problem

On the k8xp cluster, `main-cluster/postgres` sits at **588m CPU / 939Mi** against
its `1000m` / `1Gi` limits — roughly a third of the control-plane node's total CPU,
with memory nearly pinned. `pg_stat_database` attributes ~89% of all database
activity to `solidping_dev`:

| datname | xact_commit | tup_returned |
|---|---|---|
| solidping_dev | 266,456,123 | **655,300,037,218** |
| openobserve | 66,654,101 | 73,494,861,356 |
| metabase | 3,496,263 | 886,939,831 |

Inside it, one table dominates. `results` (788,546 rows / 443 MB) has taken
**2,761,150 sequential scans for 502 billion tuples read**. Measured live over
60 s: **90 seq scans and 23.6M rows per minute**, plus **511 MB/min of temp-file
spill** (14 TB and 1.35M temp files lifetime).

All of it comes from one endpoint:
`GET /api/v1/orgs/:org/checks?with=last_result,last_status_change`, which runs
**two** full scans of `results` per request — one per `with=` component.

### Why both queries seq-scan

Neither can use `results_raw_idx` (`organization_uid, check_uid, period_start DESC`
`WHERE period_type = 'raw'`). The org has **356 checks**, so the `check_uid IN (?)`
list covers ~64% of the table and the planner correctly prefers a seq scan. Both
then sort the full match set to disk. Measured with `EXPLAIN (ANALYZE, BUFFERS)`
on the live database (org `stonaltech`, 356 checks, both returning **346 rows**):

**`GetLastResultForChecks`** ([`postgres.go:2358`](../../server/internal/db/postgres/postgres.go)) — **1690 ms**

```
Unique (actual time=1404.672..1685.033 rows=346 loops=1)
  Buffers: shared hit=31080 read=3432, temp read=3080 written=3089
  ->  Sort (rows=502406)  Sort Method: external merge  Disk: 24640kB
        ->  Seq Scan on results (rows=502406)  Rows Removed by Filter: 287690
Execution Time: 1690.728 ms
```

**`GetLastStatusChangeForChecks`** ([`postgres.go:2395`](../../server/internal/db/postgres/postgres.go)) — **1317 ms**

```
WindowAgg (rows=346) ... Run Condition: (row_number() OVER w1 <= 1)
  ->  WindowAgg (rows=502361)   -- LAG(status) OVER (PARTITION BY check_uid ...)
        ->  Sort (rows=502361)  Sort Method: external merge  Disk: 17744kB
              ->  Seq Scan on results (rows=721445)  Rows Removed by Filter: 68583
Execution Time: 1317.270 ms
```

The comment at [`postgres.go:2367`](../../server/internal/db/postgres/postgres.go)
claims the org predicate "rides the `results_raw_idx` partial index". It does not,
and has not at this data volume.

### The amplifier: an invalidation storm

The endpoint is called **153×/5 min (0.51/s)** by a *single* open dashboard tab.
[`LiveEventsContext.tsx:118`](../../web/dash0/src/contexts/LiveEventsContext.tsx)
invalidates `orgRoot("checks")` / `infiniteOrgRoot("checks")` on every `results`
live event — deliberately, so "last checked" doesn't go stale (the comment says as
much). But check workers write results continuously, so in steady state the list
refetches essentially without pause.

0.51 req/s × ~1 s of database CPU per request ≈ the observed 588m. **One browser
tab is burning half a CPU core**, and this scales linearly with concurrent viewers
and super-linearly with `results` growth.

### `GetLastStatusChangeForChecks` is also wrong

The LAG query treats the first row of each `check_uid` partition as a transition
(`prev_status` is NULL, and `status IS DISTINCT FROM NULL` is true). Once raw
results age out and are compacted into `hour`/`day` rows, the oldest *surviving*
raw row becomes that first row — so for any check that has not genuinely changed
status inside the raw-retention window, the API returns **the retention horizon**,
which slides forward on every compaction run.

Verified on live data — `api.stonal.io (ssl)` has four surviving raw rows, all
`status = 3`:

```
 2026-08-09 15:17:31 | default | 3
 2026-08-09 09:17:31 | default | 3
 2026-08-09 03:17:31 | default | 3
 2026-08-08 21:17:31 | default | 3   <-- reported as "last status change"
```

`checks.status_changed_at` for the same check is **2026-07-06 01:38:58** — the
real answer. The dashboard renders this as `<LiveDuration since=... />`
([`check-summary-cards.tsx:85`](../../web/dash0/src/components/checks/check-summary-cards.tsx)),
so it currently shows "up for 18 hours" for a check that has been up for 34 days.

Across the org, comparing the query's output to `checks.status_changed_at` for the
435 checks with raw history:

| | count |
|---|---|
| checks compared | 435 |
| query result == oldest surviving raw row (retention artifact) | **102 (23%)** |
| `status` agrees with `checks.status` | 425 (98%) |
| `status_changed_at` within 5 min of the query's answer | 77 (18%) |
| `checks.status_changed_at` IS NULL | 41 |

The two columns already on `checks` — `status` and `status_changed_at`
([`check.go:157-159`](../../server/internal/db/models/check.go)) — are maintained
on the incident path by `deriveCheckStatus`
([`incidents/service.go:229`](../../server/internal/handlers/incidents/service.go))
and persisted by `UpdateCheckStatusAndClocks`
([`postgres.go:3072`](../../server/internal/db/postgres/postgres.go)). They carry
the *derived* check status, which respects confirmation/recovery periods and
flapping — i.e. exactly what the dashboard already displays as the check's status,
and the correct partner for a "changed at" timestamp next to it.

## Proposal

Three independent changes, in decreasing order of leverage. Each is shippable
alone.

### 1. Rewrite `GetLastResultForChecks` as a per-check index lookup

Instead of scanning and sorting every raw row for the org, do one index descent
per check. Verified on the live database:

```
Nested Loop (actual time=2.025..47.223 rows=346 loops=1)
  Buffers: shared hit=1070 read=344
  ->  Limit (actual time=0.131..0.131 rows=0.97 loops=356)
        ->  Index Scan using results_raw_idx on results
              Index Cond: ((organization_uid = $1) AND (check_uid = cu.uid))
              Index Searches: 356
Execution Time: 47.377 ms
```

**47 ms vs 1690 ms — 36× faster, 24× fewer buffers, zero temp spill**, identical
346-row output. No schema change, no migration, no semantic change.

- **Postgres**: `CROSS JOIN LATERAL` over the supplied check UIDs (`unnest($2::uuid[])`
  or `VALUES`), with `ORDER BY period_start DESC LIMIT 1` in the subquery so the
  index provides the ordering.
- **SQLite** ([`sqlite.go:2306`](../../server/internal/db/sqlite/sqlite.go)) has no
  `LATERAL`; use a correlated scalar subquery selecting the winning `uid` per check,
  joined back to `results`. Same shape as the existing CTE-then-join structure, so
  the `r.*` scan target is unaffected. Confirm SQLite picks the equivalent index
  rather than silently regressing to a scan.
- Delete the stale "rides the `results_raw_idx` partial index" comment, or make it
  true.

### 2. Serve `last_status_change` from `checks`, and delete the LAG query

Drop `GetLastStatusChangeForChecks` from the list path entirely. `ListChecks`
already has the `checks` rows in hand — populate `LastStatusChange` from
`check.Status` / `check.StatusChangedAt` with no additional query at all
(`ListChecks` [`service.go:916-935`](../../server/internal/handlers/checks/service.go),
`GetCheck` [`service.go:1464`](../../server/internal/handlers/checks/service.go)).

This removes the second full scan, fixes the retention-horizon bug, and aligns the
timestamp with the status the UI displays beside it.

Two things it changes, both of which need to be stated in the spec's PR and in
`web/docs`:

- **Semantics shift** from "last raw probe-status transition" to "last derived
  check-status change". A single unconfirmed blip no longer resets the timer. This
  is the intended reading of the UI copy ("up for X"), but it *is* an observable
  API behavior change.
- **`StatusChangedAt` is nullable** and is NULL for 41/435 checks here — checks
  that have never transitioned since the column was introduced. Decide and
  document the contract: either omit `lastStatusChange` when NULL (current
  behavior when a check has no history) or fall back to `created_at`. Do **not**
  fall back to the old query.

If the raw-probe-transition semantics turn out to be wanted somewhere, that is a
separate, explicitly-scoped endpoint — not a field silently attached to every
list row. Do not reintroduce it into the list path.

### 3. Stop refetching the whole list on every result write

Even at 47 ms, refetching 356 checks twice a second to reflect one probe result is
waste — HTTP, JSON serialization, and React re-render on top of the database.
Change [`LiveEventsContext.tsx:118`](../../web/dash0/src/contexts/LiveEventsContext.tsx)
so a `results` event no longer invalidates the checks-list roots. Preferred: have
the realtime `results` event carry the new `lastResult` (and status/`statusChangedAt`
when the transition actually fires `KindChecks`) so the client patches the cached
list row in place. Failing that, throttle/coalesce the invalidation to a few
seconds — the existing test at
[`LiveEventsContext.test.ts:253`](../../web/dash0/src/contexts/LiveEventsContext.test.ts)
pins today's behavior and must be updated deliberately, not deleted.

## Resolved open questions

> **Q (§2):** "`StatusChangedAt` is nullable and is NULL for 41/435 checks here —
> checks that have never transitioned since the column was introduced. Decide and
> document the contract: either omit `lastStatusChange` when NULL (current behavior
> when a check has no history) or fall back to `created_at`. Do **not** fall back to
> the old query."
>
> **Decision:** **Omit the field.** When `checks.status_changed_at` is NULL, do not
> emit `lastStatusChange` in the response at all — no fallback to `created_at`, and
> no fallback to the old LAG query. This matches today's behavior for a check with
> no history, so clients already handle the absent field. Document this explicitly
> in the `web/docs` API reference and in the `openapi.yaml` field description
> (acceptance criterion 5): *`lastStatusChange` is absent for checks that have never
> recorded a status transition.*

## Out of scope

- **Raw-results retention and `results` growth.** 720,601 raw rows back to
  2026-07-12; shrinking retention would shrink the scans but treats the symptom.
- **Postgres tuning on k8xp** (`work_mem` is the 4 MB default, `shared_buffers`
  256 MB against a 443 MB hot table). Raising `work_mem` would remove the temp
  spill today, but that is a k8xp-repo change and is not a substitute for #1/#2 —
  after them, nothing in this path sorts at all.
- The other high-`seq_scan` tables in `solidping_dev` (`check_groups` at 101.7M
  scans, `check_jobs` at 29.7M, `workers` at 29.4M). All are tiny (≤490 rows) so
  the per-scan cost is negligible; the scan *counts* suggest hot-loop N+1 patterns
  worth a separate look, but they are not what is burning the CPU.

## Acceptance criteria

- [ ] `GET /checks?with=last_result,last_status_change` performs **zero**
      sequential scans of `results`; verified by reading `pg_stat_user_tables.seq_scan`
      for `results` before and after a burst of requests.
- [ ] `pg_stat_database.temp_bytes` for the app database stops growing during
      steady-state dashboard polling.
- [ ] `GetLastResultForChecks` returns byte-identical results to today on both
      Postgres and SQLite, at <100 ms for a 356-check org.
- [ ] `lastStatusChange.time` for a check that has been up continuously reports
      its real transition time, not the oldest surviving raw row. Regression test:
      seed a check whose only surviving raw rows share one status, assert the
      response does **not** return the oldest row's timestamp.
- [ ] The NULL-`StatusChangedAt` contract is decided, implemented, and documented.
- [ ] A `results` live event no longer triggers a full checks-list refetch;
      "last checked" still updates within one poll interval.
- [ ] `web/docs` API reference updated for the `last_status_change` semantics
      change.

## Implementation Plan

1. **`server/internal/db/postgres/postgres.go`** — rewrite `GetLastResultForChecks`
   (2358) as `CROSS JOIN LATERAL`; delete `GetLastStatusChangeForChecks` (2395).
2. **`server/internal/db/sqlite/sqlite.go`** — mirror both (2306, 2354), per the
   sync-pg-to-sqlite convention.
3. **`server/internal/db/service.go`** — drop `GetLastStatusChangeForChecks` (319)
   from the DB service interface; `GetLastResultForChecks` (316) keeps its
   signature.
4. **`server/internal/handlers/checks/service.go`** — populate `LastStatusChange`
   from the in-hand `models.Check` in both `ListChecks` (916) and `GetCheck` (1464);
   handle NULL `StatusChangedAt` per the decided contract. `IncludeLastStatusChange`
   stays as a response-shaping flag but costs no query.
5. **`web/dash0/src/contexts/LiveEventsContext.tsx`** — drop the checks-list roots
   from the `results` invalidation map; update
   `LiveEventsContext.test.ts:253` and its comment to pin the new contract.
6. **Tests** — a Postgres-backed test asserting `seq_scan` on `results` is unchanged
   across a `ListChecks` call; the retention-horizon regression test above;
   parity tests for `GetLastResultForChecks` across both backends.
7. **Docs** — `web/docs` API reference for `with=last_status_change`;
   `server/internal/app/openapi/openapi.yaml` field description.

Nothing here requires a migration: `checks.status` and `checks.status_changed_at`
already exist and are already maintained.
