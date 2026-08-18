-- Parity only: re-create the `abandoned` boolean and fold status 9 back into
-- "error + abandoned". Run the column back in BEFORE the conversion, since the
-- conversion writes to it.

alter table results add column if not exists abandoned boolean not null default false;

--bun:split

comment on column results.abandoned is
  'True when the abandoned-result reaper finalized this row from a stale created marker (spec 2026-08-18-03): status becomes error, but the row is excluded from availability unlike a genuine error.';

--bun:split

update results
set status = 6,
    abandoned = true
where period_type = 'raw'
  and status = 9;
