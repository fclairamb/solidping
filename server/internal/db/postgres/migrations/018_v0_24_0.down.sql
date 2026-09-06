-- Teardown/parity half of the consolidated v0.24.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of
-- 018_v0_24_0.up.sql.

-- ==========================================================================
-- SECTION: worker-slug-leading-digit
--
-- Restores the leading-letter requirement. This FAILS if any worker has
-- registered under a digit-leading slug since the up migration ran, and that
-- is the right outcome for a parity-only down: a downgraded binary's
-- WorkerSlugPattern would refuse that identity at boot anyway, and silently
-- deleting a live worker's row would be worse than a loud constraint error.
-- ==========================================================================

alter table workers drop constraint workers_slug_check;

--bun:split

alter table workers add constraint workers_slug_check
  check (slug ~ '^[a-z][a-z0-9-]{2,20}$');
