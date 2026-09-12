-- Teardown/parity half of the consolidated v0.28.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of
-- 021_v0_28_0.up.sql.

-- ==========================================================================
-- SECTION: check-name-backfill
--
-- Deliberately NOT reversed. The up-migration copied each nameless check's
-- slug into its name; nothing records which rows were blank beforehand, and
-- re-blanking every name equal to its slug would wipe names operators chose.
-- A schema rollback does not need it either — the column is unchanged.
-- ==========================================================================

select 1;
