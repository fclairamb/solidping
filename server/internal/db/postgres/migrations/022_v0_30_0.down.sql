-- Teardown/parity half of the consolidated v0.30.0 migration — never run in
-- production. Sections appear in the EXACT REVERSE order of
-- 022_v0_30_0.up.sql.

-- ==========================================================================
-- SECTION: user-contact-dm-channel-id
-- ==========================================================================

alter table user_contacts drop column if exists dm_channel_id;

-- ==========================================================================
-- SECTION: user-contact-team-id
-- ==========================================================================

alter table user_contacts drop column if exists team_id;
