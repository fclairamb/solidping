-- Teardown/parity half of 016_v0_18_0.up.sql — never run in production.
-- Sections appear in the EXACT REVERSE order of the up file.

-- ==========================================================================
-- SECTION: support-inbox
-- Teardown half of the support-inbox section (spec 2026-08-22-02).
-- ==========================================================================

-- LOSSY, and unusually so: this drops every captured human message. There is no
-- downgrade that keeps them — the whole point of the feature is that these rows
-- exist nowhere else.
drop table if exists support_messages;

--bun:split

drop table if exists support_threads;
