-- v0.38.5 — SQLite twin of the Postgres 028_v0_38_5 migration.
--
-- These sections were first appended to 027_v0_39_0 after it had shipped in
-- v0.38.1, and released that way in v0.38.4. 027 is back to its released
-- content. A database that ran the v0.38.4 copy of 027 has these columns
-- already: the migration guard marks this migration applied for it
-- (migrationguard.Superseded), because SQLite cannot add a column only if it
-- is missing.
--
-- Frozen once tagged, like every migration: a later change goes in 029.

-- SECTION: check-job-run-request (spec 2026-10-12-05)
-- A "Run now" (or a check edit) that lands while a worker holds the job is
-- parked here; every release makes the job due at it, then clears it.
alter table check_jobs add column run_requested_at text; -- Pending run request recorded while the job was leased; NULL: none pending

--bun:split

-- SECTION: incident-reminders (spec 2026-10-12-16)
-- Re-notify an open, unacknowledged incident every interval. NULL on the
-- check inherits the org parameter incidents.reminder_interval_seconds
-- (absent = 0 = off). reminder_count is the compare-and-set token.
alter table checks add column reminder_interval_seconds integer; -- NULL: inherit org default; 0: off; else 900..86400 seconds

--bun:split

alter table incidents add column reminder_count integer not null default 0; -- reminders sent, bumped by compare-and-set

--bun:split

alter table incidents add column last_reminder_at text; -- when the last reminder went out; NULL: none yet
