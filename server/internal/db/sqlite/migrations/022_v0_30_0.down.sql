-- Teardown/parity half of the consolidated v0.30.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of
-- 022_v0_30_0.up.sql.

-- ==========================================================================
-- SECTION: user-contact-dm-channel-id
--
-- SQLite has supported `ALTER TABLE ... DROP COLUMN` since 3.35; the column
-- carries no index and no constraint, so no table rebuild is needed.
-- ==========================================================================

alter table user_contacts drop column dm_channel_id;

-- ==========================================================================
-- SECTION: user-contact-team-id
--
-- SQLite has supported `ALTER TABLE ... DROP COLUMN` since 3.35; the column
-- carries no index and no constraint, so no table rebuild is needed.
-- ==========================================================================

alter table user_contacts drop column team_id;
