-- v0.17.0 — the abandoned-result reaper: a dedicated ResultStatusAbandoned (9)
-- status value plus the partial index the reaper scans on
-- (specs 2026-08-18-03 and 2026-08-18-10, consolidated).
--
-- Numbering: 013 is the last RELEASED migration, so 014 is the next number for
-- the upcoming release. This file replaces the pair 015 + 016 that the v0.17.0
-- cycle developed against; 015 added a `results.abandoned` boolean and 016
-- immediately dropped it again after folding it into the status enum, so every
-- fresh install created a column only to destroy it (and, on SQLite, rebuilt
-- the largest table in the system for no net gain). v0.17.0 is unreleased, so
-- the series is consolidated into this single file — see
-- wiki/conventions/database.md. Development databases that ran 014/015/016
-- must be RESET, not repaired.
--
-- WHY THE REAPER EXISTS
--
-- A raw result left in ResultStatusCreated never gets a matching terminal
-- write when the process that claimed it dies before finishing. The most
-- visible source today is CreateCheck's one-time "Check created" marker row:
-- the aggregation job deliberately never deletes a lifecycle-marker row (it
-- is "intentionally preserved" so a permalink never 404s — see
-- job_aggregation.go's measurableSourceUIDs), so once every later raw row
-- ages out of retention the marker becomes the newest raw row again and
-- "Last checked" reads it verbatim — a check that stopped reporting three
-- weeks ago showing "Last checked 20d ago" next to "Currently up for 2d 10h".
--
-- WHY ONLY `created` IS REAPED, NEVER `running`
--
-- models.ResultStatus.IsLifecycleMarker groups both as non-measurement
-- statuses for availability purposes, but `running` is also used by heartbeat
-- checks (handlers/heartbeat) as a legitimate, externally-reported, possibly
-- long-lived status with no check_jobs period/lease for a "plausible execution
-- window" to be measured against — reaping it would finalize a heartbeat's
-- genuine in-progress report into a fake error.
--
-- WHY A STATUS RATHER THAN A FLAG
--
-- The first cut encoded a reaped attempt as "status = error, plus an invisible
-- asterisk" (`abandoned = true`). The two axes never actually vary
-- independently — the reaper is the flag's only writer, the state is terminal,
-- and there is no "abandoned but some other status" combination — so the flag
-- collapses into one more case of the status enum it was shadowing. Rendering
-- a reaped attempt as `error` was also less honest than a status of its own:
-- everywhere else `error` means the attempt ran and failed, and a reader then
-- had to know about a second column to learn that this particular error is
-- quietly un-counted from availability.
--
-- Status 9 is excluded from availability math (successful_checks /
-- total_checks) everywhere it is computed — see
-- Result.ExcludedFromAvailability, the one predicate the hour rollup, the
-- uptimebar union and the availability service all route through so they
-- cannot drift apart. Counting a reaped attempt as downtime would manufacture
-- an outage out of OUR worker's crash, not the monitored service's.
--
-- WHY THE PARTIAL INDEX
--
-- It backs the reaper's own candidate scan: the reaper is looking for a
-- handful of stuck marker rows inside `results`, the largest table in the
-- system, and must never fall back to a sequential scan of it. It covers only
-- status=1 (created) — status=2 (running) is excluded on purpose (see above),
-- and heartbeat checks can write many long-lived running rows that this index
-- must not have to carry.
--
-- There is NO data conversion step here. A database applying this migration
-- has never had an `abandoned` column, so there is nothing to convert.

-- 1. Widen `status in (0..8)` to `(0..9)`. The constraint is an unnamed inline
--    column check from 001 (released and frozen, so it cannot be edited), so
--    Postgres named it itself; rather than betting on `results_status_check`,
--    drop whichever check constraint on `results` actually mentions `status`
--    (there is exactly one — the only other check on the table constrains
--    period_type).
do $$
declare
  con record;
begin
  for con in
    select c.conname
    from pg_constraint c
    join pg_class t on t.oid = c.conrelid
    join pg_namespace n on n.oid = t.relnamespace
    where t.relname = 'results'
      and n.nspname = current_schema()
      and c.contype = 'c'
      and pg_get_constraintdef(c.oid) ilike '%status%'
  loop
    execute format('alter table results drop constraint %I', con.conname);
  end loop;
end
$$;

--bun:split

-- Validating (not NOT VALID): the new domain is a strict superset of the one
-- just dropped, so the scan can only confirm what is already true, but it
-- keeps the constraint indistinguishable from a freshly-created schema's — the
-- release-time golden-schema diff compares them.
alter table results
  add constraint results_status_check check (status in (0, 1, 2, 3, 4, 5, 6, 7, 8, 9));

--bun:split

create index idx_results_lifecycle_pending on results (period_start)
  where period_type = 'raw' and status = 1;

--bun:split

-- 001's comment on this column has been wrong since the enum was renumbered;
-- refresh it now that 9 joins the domain.
comment on column results.status is
  '1=created, 2=running, 3=up, 4=down, 5=timeout, 6=error, 7=degraded (aggregated only), 8=warning, 9=abandoned (reaper-minted, excluded from availability).';
