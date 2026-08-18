-- Parity only: drop the reaper's index and narrow the status CHECK domain back
-- to what 013 left behind. Any row the reaper minted has to be folded back
-- into a value the narrower domain accepts before the constraint can be
-- re-added — plain `error` is the closest pre-9 encoding, and the distinction
-- this release introduced is simply lost on the way down.

drop index if exists idx_results_lifecycle_pending;

--bun:split

update results
set status = 6
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
