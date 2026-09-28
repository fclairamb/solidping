-- v0.34.0 — the ONE consolidated SQLite migration for the (still unreleased)
-- v0.34.0 release. 024_v0_33_0 shipped in v0.33.0 and is frozen, so this is the
-- next free number and every schema change of this cycle is appended here as a
-- new SECTION, per wiki/conventions/database.md.
--
--   SECTION: capture-now-failure
--                              check_jobs.capture_failed_request_at /
--                              capture_failure_reason: a "Capture now" run
--                              that produced no screenshot is reported
--
-- ⚠️ A DEV DATABASE THAT ALREADY RAN AN EARLIER DRAFT OF THIS FILE MUST BE
-- RESET, NEVER REPAIRED. bun keys an applied migration on its numeric prefix
-- alone, so appending a section to 025 does not re-run it: set
-- `SP_DB_RESET=true` (test/demo run mode) or delete the SQLite file.
-- **Do not run `solidping migrate repair`** — it rewrites the recorded
-- checksum without applying anything.

-- ==========================================================================
-- SECTION: capture-now-failure  (spec 2026-09-27-01)
--
-- See the Postgres twin for the rationale. SQLite has no
-- `add column if not exists`; this runs once on a database that has never
-- seen 025. Both columns are nullable, so the add is a metadata-only change.
-- ==========================================================================

alter table check_jobs add column capture_failed_request_at text; -- Requested-at of the newest "Capture now" request whose run produced no screenshot

--bun:split

alter table check_jobs add column capture_failure_reason text; -- Why that request produced no screenshot, as reported by the run
