-- Parity only: re-create the `abandoned` boolean, fold status 9 back into
-- "error + abandoned", and narrow the status CHECK domain again. Reverse order
-- of the up migration: the column has to exist before the conversion writes to
-- it, and the domain can only be narrowed once no row carries 9.

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

--bun:split

alter table results drop constraint if exists results_status_check;

--bun:split

alter table results
  add constraint results_status_check check (status in (0, 1, 2, 3, 4, 5, 6, 7, 8));

--bun:split

comment on column results.status is
  '0=initial, 1=up, 2=down, 3=timeout, 4=error, 5=running (raw only).';
