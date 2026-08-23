-- Teardown/parity half of 016_v0_18_0.up.sql (SQLite) — never run in
-- production. Sections appear in the EXACT REVERSE order of the up file.

-- ==========================================================================
-- SECTION: support-inbox
-- Teardown half of the support-inbox section (spec 2026-08-22-02).
-- ==========================================================================

-- LOSSY: drops every captured human message. See the Postgres file.
drop table if exists support_messages;

--bun:split

drop table if exists support_threads;
