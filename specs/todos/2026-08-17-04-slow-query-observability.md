---
model: sonnet
effort: medium
---

# A slow SQL query is invisible in production

## Problem

Diagnosing the 2026-08-16 status-page slowdown on `solidping.k8xp.com` (see
[spec 2026-08-17-03](specs/todos/2026-08-17-03-uptimebar-queries-seq-scan-results-table.md))
required going outside the product entirely: `kubectl exec` into the Postgres
primary and poll `pg_stat_activity` in a 50 ms loop to catch the offending
statement in flight. Nothing in SolidPing's own telemetry pointed at it, even
though the query took 530–2400 ms and ran on every status-page render.

The query hook already exists and is wired into both backends —
[sloghook/hook.go](server/internal/db/sloghook/hook.go),
[postgres.go:189](server/internal/db/postgres/postgres.go:189),
[sqlite.go:174](server/internal/db/sqlite/sqlite.go:174) — but it has two gaps
that together make a slow query undetectable:

**1. Successful queries log at DEBUG, regardless of how slow they are**
([hook.go:73](server/internal/db/sloghook/hook.go:73)):

```go
slog.LogAttrs(ctx, slog.LevelDebug, "SQL query", attrs...)
```

Production runs at INFO, so a 2.4 s `SELECT` produces no log line at all. Only
*failures* get WARN. Raising the global level to DEBUG is not a workaround —
this instance runs ~831k raw inserts per 24 h, so DEBUG would emit millions of
lines per day and bury the signal it was turned on to find.

**2. The Prometheus histogram has no callsite attribution**
([hook.go:51](server/internal/db/sloghook/hook.go:51)):

```go
prommetrics.RecordDBQuery(operation, h.Backend, duration.Seconds(), !failed)
```

The only labels are `operation` (`SELECT`) and `backend` (`postgres`). Every
read in the product shares the `SELECT` bucket, so the histogram can show that
*some* selects got slower while giving no way to tell which one — exactly the
question that mattered.

The HTTP layer's `duration=` log line did show the symptom (`/status-pages/…
duration=9.36s`), but it stops at the request boundary: it cannot say whether
the time went to SQL, and if so to which statement.

## Proposal

Two narrow changes to `sloghook`, plus one dashboard-facing metric. Deliberately
**not** a tracing project — `SP_OTEL_ENABLED` already covers deep per-span
analysis when someone opts in; this is about the default-on path.

### 1. Log slow queries at WARN

Add a duration threshold to `QueryHook`. A successful query slower than the
threshold logs at **WARN** with the same attrs the failure path already builds,
plus the statement text (see redaction note below):

```go
switch {
case failed:                     // unchanged — WARN
case duration >= h.SlowThreshold: // new — WARN "slow SQL query"
default:                          // unchanged — DEBUG
}
```

- Configurable via koanf as `db.slow_query_threshold` / `SP_DB_SLOW_QUERY_THRESHOLD`,
  following the existing parameter-key convention (dots for hierarchy,
  snake_case within a segment).
- Default **500 ms**. That is comfortably above healthy queries here (the
  post-fix uptime-bar tiers measure ~10 ms and ~97 ms) and below anything a user
  would call slow. `0` disables.
- Note the koanf multi-word-key quirk when adding the env binding: `SP_*` vars
  for multi-word koanf keys need the manual reader, same as
  `rate_limiting` / `shutdown_timeout`.
- **Rate-limit the WARN.** If a slow query is on a hot path it is slow on *every*
  request; unthrottled this becomes its own log flood. Emit at most once per
  (normalized statement) per interval — suggest 1 min — and include a
  `suppressed=<n>` count, so a pathological query produces a steady trickle
  rather than a wall.

**Redaction.** `Verbose` exists precisely because statements may carry sensitive
values, and it defaults off. The slow-query line must not become a backdoor that
leaks them: log the statement with argument values stripped (bun exposes the
templated query; normalize literals to `?`), or gate the full text behind the
existing `Verbose` flag and log only the normalized form otherwise. Whichever is
chosen, add a test asserting a known secret value never reaches the log line
with `Verbose=false`.

### 2. Give the histogram a callsite label

Add a low-cardinality `callsite` (or `table`) label to `prommetrics.RecordDBQuery`
so `SELECT` splits into meaningful series.

Cardinality is the whole risk here — the label must come from a **bounded**
set, never from the raw SQL string. Prefer a value threaded through `ctx` by the
calling package (e.g. `uptimebar.bucket_availability`, `results.list`), falling
back to a constant like `unlabelled` when absent. That keeps the series count
proportional to the number of annotated call paths rather than to traffic. Start
by labelling only the known-hot read paths — `uptimebar`, `results`, `checks`,
`statuspages` — rather than instrumenting everything at once.

### 3. Alert-ready signal for the table that caused this

The underlying trigger was `results` outgrowing Postgres `shared_buffers`
(317 MB heap vs 256 MB), which turned a tolerable seq scan into a disk-bound
one. Expose a gauge for `results` row count by `period_type`, refreshed on the
existing aggregation-job cadence rather than per-request (the count is not free).
This makes ingest growth visible *before* it crosses a cache cliff, and would
have framed this incident correctly in one glance.

## Verification

- A seeded query slower than the threshold produces exactly one WARN line
  containing the normalized statement and its duration; a fast one produces
  none at INFO. Table-driven, both backends.
- With `Verbose=false`, a statement carrying a secret literal is logged without
  that literal.
- Repeated slow queries within the throttle interval collapse to one line
  carrying `suppressed`.
- The histogram exposes distinct `callsite` series for at least the `uptimebar`
  read path, and total series count stays bounded when the same path is hit with
  many different argument values.

## Out of scope

`pg_stat_statements` is **available but not installed** on the k8xp cluster
(`SELECT * FROM pg_stat_statements` errors with "relation does not exist"),
which is why per-statement history was unavailable during this incident. That is
an infrastructure change in the `k8xp` repo (CNPG
`spec.postgresql.shared_preload_libraries` plus `CREATE EXTENSION`), not a
SolidPing code change — tracked separately.
