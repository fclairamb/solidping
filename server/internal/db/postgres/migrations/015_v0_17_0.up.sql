-- v0.17.0 — reap abandoned created/running raw results (spec 2026-08-18-03).
--
-- A raw result left in a lifecycle-marker status (created/running, see
-- models.ResultStatus.IsLifecycleMarker) never gets a matching terminal write
-- when the process that claimed it dies before finishing. The most visible
-- source today is CreateCheck's one-time "Check created" marker row: the
-- aggregation job deliberately never deletes a lifecycle-marker row (it is
-- "intentionally preserved" so a permalink never 404s — see
-- job_aggregation.go's measurableSourceUIDs), so once every later raw row
-- ages out of retention the marker becomes the newest raw row again and
-- "Last checked" reads it verbatim — a check that stopped reporting three
-- weeks ago showing "Last checked 20d ago" next to "Currently up for 2d 10h".
--
-- `abandoned` distinguishes a row the reaper finalized this way from a
-- genuine ResultStatusError: same terminal status 6 (the timeline keeps
-- honest evidence an attempt happened and nothing more is known), but
-- excluded from availability math (successful_checks / total_checks)
-- everywhere it is computed — see Result.ExcludedFromAvailability, the one
-- predicate the hour rollup, the uptimebar union, and the availability
-- service all route through so they cannot drift apart. Counting a reaped
-- attempt as downtime would manufacture an outage out of OUR worker's crash,
-- not the monitored service's.
--
-- The partial index backs the reaper's own candidate scan: it is looking for
-- a handful of stuck marker rows inside `results`, the largest table in the
-- system, and must never fall back to a sequential scan of it.

alter table results add column abandoned boolean not null default false;

--bun:split

comment on column results.abandoned is
  'True when the abandoned-result reaper finalized this row from a stale created/running marker (spec 2026-08-18-03): status becomes error, but the row is excluded from availability unlike a genuine error.';

--bun:split

create index idx_results_lifecycle_pending on results (period_start)
  where period_type = 'raw' and status in (1, 2);
