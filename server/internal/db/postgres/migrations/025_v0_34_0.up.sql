-- v0.34.0 — the ONE consolidated Postgres migration for the (still unreleased)
-- v0.34.0 release. 024_v0_33_0 shipped in v0.33.0 and is frozen, so this is the
-- next free number and every schema change of this cycle is appended here as a
-- new SECTION, per wiki/conventions/database.md.
--
--   SECTION: capture-now-failure
--                              check_jobs.capture_failed_request_at /
--                              capture_failure_reason: a "Capture now" run
--                              that produced no screenshot is reported
--
-- Every pod runs migrations on boot, unlocked, so every statement here is
-- additive and re-runnable (nullable columns, `if not exists`).
--
-- ⚠️ A DEV DATABASE THAT ALREADY RAN AN EARLIER DRAFT OF THIS FILE MUST BE
-- RESET, NEVER REPAIRED. bun keys an applied migration on its numeric prefix
-- alone, so appending a section to 025 does not re-run it: set
-- `SP_DB_RESET=true` (test/demo run mode) or drop and recreate the database.
-- **Do not run `solidping migrate repair`** — it rewrites the recorded
-- checksum without applying anything.

-- ==========================================================================
-- SECTION: capture-now-failure  (spec 2026-09-27-01)
--
-- A "Capture now" run whose result came back without a screenshot used to be
-- silent: the dashboard polled for 3 minutes and then gave up. The result
-- submission now records, on the job row whose lease carried the request,
-- which request failed (its requested-at, the value the API returned to the
-- dashboard) and why. The screenshot listing exposes the newest one across the
-- check's jobs. A successful capture writes nothing: its image in the listing
-- is the answer. NULL is the norm.
-- ==========================================================================

alter table check_jobs add column if not exists capture_failed_request_at timestamptz;

--bun:split

comment on column check_jobs.capture_failed_request_at is
  'Requested-at of the newest "Capture now" request whose run produced no screenshot (spec 2026-09-27-01). NULL when none failed.';

--bun:split

alter table check_jobs add column if not exists capture_failure_reason text;

--bun:split

comment on column check_jobs.capture_failure_reason is
  'Why the "Capture now" request in capture_failed_request_at produced no screenshot, as reported by the run.';
