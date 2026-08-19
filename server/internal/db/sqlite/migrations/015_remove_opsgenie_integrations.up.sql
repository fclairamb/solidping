-- SQLite mirror of postgres/migrations/015_remove_opsgenie_integrations.up.sql
-- — remove the sunsetting Opsgenie integration type, replaced by PagerDuty
-- (spec 2026-08-19-02: Atlassian is retiring Opsgenie in April 2027). Hard
-- delete, not a tombstone: the product is pre-1.0 and keeping a dead
-- connection_type around would force every type switch in the code to carry
-- a zombie case forever.
--
-- Numbering: this is a scratch migration for the still-unreleased v0.17.0
-- cycle (013 is the last RELEASED migration; 014_v0_17_0 is that cycle's
-- consolidated file so far). See the Postgres file for the full numbering
-- rationale and the FK graph this deletion order relies on:
--   - check_channels.integration_uid              ON DELETE CASCADE  (automatic)
--   - user_integration_identities.integration_uid  ON DELETE CASCADE  (automatic, 011_v0_14_0)
--   - incident_notifications.connection_uid        ON DELETE SET NULL (automatic — keeps audit history)
--   - escalation_policy_targets.target_uid          NOT a foreign key (polymorphic
--     column shared by every target_type), so a 'connection' target pointing at an
--     opsgenie integration is deleted explicitly below.
--
-- No PRAGMA foreign_keys toggling needed: this only deletes rows, it does not
-- rebuild any table.

delete from escalation_policy_targets
where target_type = 'connection'
  and target_uid in (select uid from integrations where type = 'opsgenie');

--bun:split

delete from integrations where type = 'opsgenie';
