-- solidping — status pages: explicit history period enum (spec 2026-06-30-03).
-- SQLite mirror of the Postgres migration. Adds a self-describing history_period
-- column (24h | 7d | 30d | 90d) mirroring the badge uptime-bar vocabulary, so
-- status pages can render a 24h hourly view. history_days is kept populated for
-- one release for backward-compat.

alter table status_pages add column history_period text not null default '90d'; -- 24h (hourly) | 7d | 30d | 90d

-- Backfill from the existing day count: 7→7d, 30→30d, everything else (incl. 90)→90d.
update status_pages
set history_period = case history_days
  when 7 then '7d'
  when 30 then '30d'
  else '90d'
end;
