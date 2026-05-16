# Batched DB inserts/updates with a 500ms delay — why we're not doing it

## Question

Should SolidPing buffer database inserts and updates in memory and flush them
in batches, delayed by up to 500ms, to improve write throughput?

## Decision

**No**, as of 2026-05-16. Documented here so the next person tempted by this
idea finds the prior reasoning first, and either accepts it or comes back with
a profile that overturns it.

## Why not

### 1. The cost lands directly on the product's value proposition

SolidPing's job is to tell you about freshness of state — check results,
heartbeats, incidents. A 500ms write buffer means:

- **Result submission → incident detection**: up to 500ms of added latency
  on the path the product is literally selling.
- **Heartbeats**: a "we're alive" beacon delayed by half a second silently.
  Deadline-based heartbeat checks in that window will fire false-positive
  incidents.
- **Read-after-write breaks**: dashboards, public status pages, the
  `client results list` CLI, and any handler that polls right after a
  worker submits will race the buffer. "I just saw a green check 200ms ago,
  why does the API still say red?" becomes a recurring class of bug report.

### 2. Crash window is a silent data-loss surface

Anything sitting in the in-memory buffer at the moment the process is killed
(SIGKILL, OOM, panic, k8s eviction) is gone. For result rows or audit/event
records, that's a silent gap with no error returned to the worker that
submitted it. The graceful-shutdown drain helps the friendly path, not the
unfriendly one.

### 3. The premise is unproven

Write rate is bounded by `checks × frequency`. Even 10k checks at a 30s
interval averages ~330 writes/sec — well within plain PostgreSQL and
SQLite-in-WAL-mode territory. Nothing in current telemetry or the codebase
points to writes as the bottleneck. The likely real culprits, in order:

- N+1s and missing eager loads in dashboard handlers.
- Missing or unused indexes on result/event queries.
- Worker poll chattiness.
- Connection-pool sizing.

Adding a 500ms buffer to "speed up inserts" before measuring is solving an
unconfirmed problem with a tool that has real downsides.

### 4. Complexity tax is permanent

A batching layer is not a small piece of code in practice:

- In-memory buffer with bounded capacity.
- Periodic flush loop + size-based flush trigger.
- Shutdown drain ordering vs. the rest of the lifecycle.
- Partial-batch failure semantics (which row failed? do we retry the batch?
  poison-pill detection?).
- Backpressure when the DB can't keep up — unbounded queue = OOM.
- Test flakiness around "did the flush happen yet" timing.

It's a long-lived surface area, and every future "stale data" bug report
will point at it first.

## Cheaper interventions to try first

If a real write hotspot is ever measured, the order of operations should be:

1. **`synchronous_commit = off`** on PostgreSQL. Gets most of the
   throughput benefit of batching with zero application code. Durability
   tradeoff (up to a few hundred ms of recently-committed transactions
   lost on crash) is *similar to* what a 500ms buffer already imposes —
   but without breaking read-after-write.
2. **Prepared statements / `pgx.Batch`** for known hot paths. Amortizes
   round-trip parsing without changing semantics.
3. **`COPY`** for genuine bulk paths (imports, migrations, backfills).
   Already the right tool for those; not relevant to steady-state writes.
4. **Index review.** Extra/unused indexes slow inserts more than batching
   speeds them up.
5. **Connection-pool tuning.** Often the actual bottleneck masquerading as
   "writes are slow."

## Conditions under which we'd reconsider

Reopen this if **all** of the following hold:

- A profile (pprof, `pg_stat_statements`, DB CPU sampling) shows a specific
  write path is the bottleneck under realistic load.
- The cheaper interventions above have been tried and are insufficient.

If we revisit, the scope must be narrow:

- One path only — the most plausible candidate is the check-results write
  path.
- Starting window ~50ms, not 500ms.
- **Never** applied to: incident state changes, auth/session writes, config
  updates, entitlement updates, anything user-facing that loops back into a
  read in the same request.

## References

- No implementation in code — this is a "we considered it and didn't do it"
  record.
- Future PRs proposing batching should link to this file and explain which
  of the conditions above are now met.
