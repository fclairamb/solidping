-- Parity only: re-create the `abandoned` flag and fold status 9 back into
-- "error + abandoned". The column has to come back before the conversion can
-- write to it.

alter table results add column abandoned integer not null default 0; -- True (1) when the abandoned-result reaper finalized this row from a stale created marker.

update results
set status = 6,
    abandoned = 1
where period_type = 'raw'
  and status = 9;
