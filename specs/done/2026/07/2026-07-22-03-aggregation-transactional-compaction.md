---
model: opus
effort: high
---

# Results compaction is not transactional — a window of raw rows survived aggregation

## Problem

On the k8xp dev deployment, check `de01b3ed-66aa-4dc0-b473-91a7ad0dff16` (org
`acmetech`) shows a compaction hole in the week view:

> https://solidping.k8xp.com/dash0/orgs/acmetech/checks/de01b3ed-66aa-4dc0-b473-91a7ad0dff16?graphPeriod=week&graphFrom=1784336400000&graphTo=1784354400000

The window is 2026-07-18 01:00–06:00 UTC. Everything before ~03:00 UTC and
after ~04:05 UTC renders as smooth hour/day rollups, but roughly
03:00–04:05 UTC still shows dense per-minute raw points — that bucket (or
buckets) was never compacted, or was only partially compacted, while its
neighbors were.

The aggregation job is structurally exposed to exactly this: in
`aggregatePeriod` (`server/internal/jobs/jobtypes/job_aggregation.go:147-250`)
the compaction of one bucket is three **separate, non-transactional** DB calls:

1. `ListResults` fetches the source rows (line 189) — the code even carries a
   `// TODO: Wrap in transaction for atomicity` at line 177;
2. `UpsertAggregatedResult` writes the rollup row (line 230);
3. `DeleteResults` removes the measurable source rows (line 235).

Failure between/among these steps leaves inconsistent states:

- crash or error after (2) but before (3) → rollup row **and** raw rows both
  exist (double counting in stats until/unless a re-run cleans up);
- error in (2) → bucket stays raw; if the error path or scheduling then stops
  making progress (job errored out, retries exhausted, pod restart), the
  bucket is stranded raw while later buckets get compacted — which matches the
  observed graph;
- new raw rows written between (1) and (3) are not in `sourceUIDs`, so the
  delete-by-UID at least can't eat unseen rows — but nothing makes the
  read-aggregate-delete triplet atomic against concurrent writers or crashes.

## Proposal

Two parts: diagnose the live occurrence, then make compaction atomic.

### 1. Diagnose the stranded window (k8xp dev)

- Query the results API / DB for that check around 2026-07-18 03:00–04:05 UTC:
  which `period_type` rows exist (raw? hour? both?), per region.
- Pull the aggregation job logs around that time (`"Aggregation error"`,
  `"Aggregation fetched rows but deleted none"`, `"Skipping marker-only
  aggregation bucket"`, job reschedule failures) to identify which step failed
  and why the bucket was skipped afterwards.
- Record the root cause in this spec (or the commit message) — don't guess.
  Note: deploys/pod restarts on solidping-dev are a plausible mid-flight kill.

### 2. Make per-bucket compaction transactional

- Add a single DB-service method (e.g. `CompactResults`) that performs
  fetch → upsert rollup → delete sources inside one `RunInTx`, implemented in
  **both** backends (`server/internal/db/postgres/postgres.go` — existing
  `UpsertAggregatedResult` ~line matching sqlite.go:1779 — and
  `server/internal/db/sqlite/sqlite.go:1779/:1978`), keeping the two in sync
  per the repo's pg↔sqlite convention. The aggregation computation can stay in
  Go (`aggregateResults`), but the write+delete must commit or roll back as
  one unit.
- Keep existing semantics: idempotent upsert on the bucket key
  (spec 2026-07-11-16 §3), lifecycle-marker rows preserved
  (`measurableSourceUIDs`), progress guard for marker-only buckets, delete
  strictly by the UIDs read in the same transaction.
- On any error the transaction rolls back → the bucket stays fully raw and a
  later run retries it cleanly; no more rollup+raw hybrid states.
- Tests (both backends, testcontainers for PG): inject a failure between
  upsert and delete and assert the rollup row is **not** committed and the raw
  rows are all still present; plus a happy-path assertion that raw rows are
  gone and exactly one rollup row exists.
- Verify the stranded 2026-07-18 window on k8xp dev gets compacted by a
  subsequent aggregation run once the fix is deployed (or after manually
  re-triggering the job).

### Open questions

- Whether the observed hole also involves a duplicate rollup (upsert committed,
  delete failed) — the diagnosis step settles which failure mode occurred.
- Whether the aggregation scheduler correctly revisits old stranded buckets;
  if `findAggregatableResults` can skip past them, that's a second bug to file
  (or fix here if small).

## Implementation Plan

Two independent code fixes plus the diagnosis, all backend-Go-only
(`internal/jobs/jobtypes` + `internal/db`).

### A. Transactional per-bucket compaction (spec Part 2 — the structural fix)

1. `internal/db/models/result.go`: add `CompactResultsOutcome` (Fetched,
   SourceCount, Compacted, DeletedCount) and the `AggregateResultsFunc` type
   (`func(sources []*Result) (rollup *Result, sourceUIDs []string, err error)`).
2. `internal/db/service.go`: add `CompactResults(ctx, filter, aggregate)
   (models.CompactResultsOutcome, error)` to the `db.Service` interface.
3. `internal/db/postgres/postgres.go` and `internal/db/sqlite/sqlite.go`
   (kept in lockstep per the pg↔sqlite convention):
   - Extract `applyResultsFilter(query, filter)` from `ListResults` so the read
     inside `CompactResults` selects exactly the same rows.
   - Extract `upsertAggregatedResultTx(ctx, tx, result)` (the idempotent
     bucket-key delete+insert) shared by `UpsertAggregatedResult` and
     `CompactResults`.
   - Implement `CompactResults`: one `RunInTx` doing fetch → `aggregate` (pure
     Go) → upsert rollup → delete exactly the returned source UIDs. Any error
     rolls the whole tx back (bucket stays fully raw). A `compactFailpoint`
     Service field (nil in production) lets same-package tests inject a failure
     between the upsert and the delete to prove rollback.
   - Add the `CompactResults` stub to the `mockDBService` in
     `internal/notifications/slack_test.go` (it asserts `var _ db.Service`).
4. `internal/jobs/jobtypes/job_aggregation.go`: replace the three separate
   `ListResults`/`UpsertAggregatedResult`/`DeleteResults` calls in
   `aggregatePeriod` with one `CompactResults` call whose `aggregate` closure
   runs `measurableSourceUIDs` + `aggregateResults`; keep the marker-only and
   "deleted none" warnings driven by the returned outcome. Delete the stale
   `// TODO: Wrap in transaction for atomicity`.

### B. Boundary-millisecond fetch exclusion (diagnosis-driven second fix)

The live diagnosis (below) shows the actual stranding mechanism: a raw row whose
`period_start` is exactly `HH:59:59.999` is found by discovery but excluded by
the step-3 fetch, which uses `period_start < periodEnd` with
`periodEnd = HH:59:59.999`. Fix: bound the fetch by the **exclusive next-bucket
start** (`periodEnd + 1ms` = `HH+1:00:00.000`) instead of the inclusive display
end, so the final-millisecond row is fetched and compacted. The stored rollup's
`period_end` display value is unchanged. This is the small "second bug" the
spec's Open Questions anticipate, and without it the transactional fix alone
would not compact the stranded window.

### C. Tests

- DB layer (both backends, testcontainers for PG): `CompactResults` happy-path
  (raw rows gone, exactly one rollup row) and rollback (failure injected between
  upsert and delete → no rollup committed, all raw rows still present).
- Jobs layer (SQLite): a raw row at `HH:59:59.999` is compacted (regression for
  the boundary bug); plus the existing marker-only / poison-pill suite must stay
  green through the `CompactResults` refactor.

### D. QA gate

`make build-backend lint-back test` (iterate with scoped
`go build`/`golangci-lint`/`go test` on `internal/jobs/...` and `internal/db/...`
first).

## Diagnosis

Investigated the live k8xp dev deployment (kubectl context `k8xp`, namespace
`solidping-dev`) on 2026-07-23.

### What was reachable

- `kubectl` cluster access works. The org in the incident (`acmetech`) is
  `organization_uid=3c9d374e-e655-431d-880c-5c161777b75c`.
- The aggregation job runs on the **main** `solidping` deployment (the
  `checks-us1`/`checks-eu2` worker deployments only execute checks).

### Confirmed root cause — boundary-millisecond fetch exclusion

The current main pod (started 2026-07-22) still logs, on essentially every
aggregation cycle:

```
msg="Found data to aggregate" check_uid=0c7e3fd6-144e-4021-b060-682b13e3e49e
    region=… period_start=2026-07-18T03:59:59.999Z source_period=raw target_period=hour
msg="No aggregation work found"
```

A **raw** row with `period_start = 2026-07-18T03:59:59.999Z` (org `acmetech`)
is still present 4.5 days later and is re-discovered on every run but never
compacted. "Found data to aggregate" immediately followed by "No aggregation
work found" — with no "Aggregating results", no rollup, and no "Skipping
marker-only" — can only be the empty step-3 fetch path
(`job_aggregation.go` `len(resultsResp.Results) == 0 → return false`).

Mechanism (code-confirmed): `findAggregatableResults` selects the newest
eligible raw row and returns its `period_start` (here `HH:59:59.999`).
`calculatePeriodBoundaries` then computes the target hour bucket as
`start = HH:00:00.000`, `end = start + 1h - 1ms = HH:59:59.999`. The step-3
targeted fetch filters `PeriodEndBefore = end` → `period_start < HH:59:59.999`
(strict). The discovered row sits **exactly** on `HH:59:59.999`, so it is
excluded from its own bucket's fetch → empty result set → `return false` → the
row is never rolled up and is re-discovered forever. Any raw row landing in the
final millisecond `[HH:59:59.999, HH+1:00:00.000)` of its hour is a permanent
stranding trap; once it becomes the newest-eligible island it is discovered on
every run yet never consumed. This is independent of transactionality and is
the "second bug" the Open Questions anticipated. Fixed here (Plan §B) by
bounding the fetch with the exclusive next-bucket start.

### Deploy/restart timeline (plausible, not the confirmed cause)

Main-deployment ReplicaSet creation times bracket the incident: rollouts at
2026-07-17 10:18, **2026-07-18 06:24**, and 2026-07-19 19:56 UTC — so the
aggregation-running pod was restarted near the incident, consistent with the
spec's "deploy is a plausible mid-flight kill" note. But with the default 24h
raw retention the 2026-07-18 03:00–04:05 buckets only become due for rollup
~2026-07-19 03:00–04:05 UTC, and no rollout landed exactly in that window, so a
deploy-mid-compaction is **not** the confirmed mechanism for this stranded row.

### What could not be inspected (honest gaps)

- Aggregation-job logs from the actual incident/compaction window
  (2026-07-18/19 03:00–04:05 UTC) are gone: the current pods started 2026-07-22,
  and `kubectl logs` only covers a pod's lifetime. No in-cluster log-aggregation
  query path was available in this environment.
- Direct DB inspection of the exact `period_type` rows for check `de01b3ed`
  (raw vs hour, per region; whether a rollup+raw **hybrid** exists) was blocked:
  the results DB is external (no in-namespace Postgres service), and the app
  container is distroless (no shell, no `psql`), so no read path was reachable
  without out-of-band credentials.
- No `"Aggregation error"` or `"Aggregation fetched rows but deleted none"`
  lines were observed in the current pod (count 0) — but that only covers
  since 2026-07-22, not the incident window.

Conclusion: the **confirmed** cause of the observed stranding is the
boundary-millisecond fetch exclusion (Plan §B), directly evidenced in live logs.
The spec's non-transactional-compaction hypothesis remains a **real, separate
structural weakness** (crash between upsert and delete → rollup+raw hybrid) that
Plan §A eliminates; whether check `de01b3ed`'s specific hole is also a hybrid
could not be confirmed without DB access. Both fixes are implemented; root-cause
attribution for `de01b3ed` specifically should be double-checked out-of-band by
someone with DB access once the fixes deploy.
