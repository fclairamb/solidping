-- Teardown/parity half of 018_v0_18_0.up.sql — never run in production.
-- Sections appear in the EXACT REVERSE order of the up file.

-- ==========================================================================
-- SECTION: must-change-password
-- Teardown half of the forced-rotation flag (spec 2026-08-23-04).
-- ==========================================================================

-- LOSSY, and lossy in the unsafe direction: dropping the column silently
-- un-forces every pending rotation, including the seeded bootstrap admin's.
-- A downgraded installation is back to a standing exposure.
alter table users drop column if exists must_change_password;
