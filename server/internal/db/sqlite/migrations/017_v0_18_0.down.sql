-- Teardown/parity half of 017_v0_18_0.up.sql — never run in production.
-- Sections appear in the EXACT REVERSE order of the up file.

-- ==========================================================================
-- SECTION: custom-domain-state
-- Teardown half of the custom-domain lifecycle columns (spec 2026-08-23-03).
-- ==========================================================================

-- SQLite has supported ALTER TABLE DROP COLUMN since 3.35; the driver in use is
-- newer, so this is a genuine mirror of the Postgres teardown rather than a
-- table rebuild.
alter table status_pages drop column custom_domain_last_check;

--bun:split

alter table status_pages drop column custom_domain_grace_since;

--bun:split

alter table status_pages drop column custom_domain_successes;

--bun:split

alter table status_pages drop column custom_domain_state;
