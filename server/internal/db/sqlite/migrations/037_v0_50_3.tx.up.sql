-- ==========================================================================
-- SECTION: status-page-history-days-bound
-- Spec 2026-11-16-01.
-- ==========================================================================
-- history_days was stored unbounded through the legacy historyDays input, and
-- the public view sized its allocations from it. The write path now refuses a
-- value outside 1..90; this rewrites the rows stored before that with the day
-- count of their history_period. Rows already within 1..90 are left alone.
update status_pages
set history_days = case history_period
    when '24h' then 1
    when '7d' then 7
    when '30d' then 30
    else 90
  end
where history_days < 1 or history_days > 90;
