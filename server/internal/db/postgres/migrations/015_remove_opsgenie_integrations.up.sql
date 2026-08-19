-- Remove the sunsetting Opsgenie integration type, replaced by PagerDuty
-- (spec 2026-08-19-02: Atlassian is retiring Opsgenie in April 2027). Hard
-- delete, not a tombstone: the product is pre-1.0 and keeping a dead
-- connection_type around would force every type switch in the code to carry
-- a zombie case forever.
--
-- Numbering: this is a scratch migration for the still-unreleased v0.17.0
-- cycle. 013 is the last RELEASED migration; 014_v0_17_0 is that cycle's
-- consolidated file so far. Per the "development workflow" in
-- wiki/conventions/database.md, additional schema/data work landing before
-- the actual release is a new numbered file (feature-named, not
-- version-named — the version name is reserved for the one file each engine
-- ships per release) and gets folded into the final NNN_vX_Y_Z at
-- consolidation time, same as 015+016 were folded into 014 earlier in this
-- same cycle.
--
-- FK graph around integrations(uid) (see 001_v0_1_0.up.sql):
--   - check_channels.integration_uid              ON DELETE CASCADE  (automatic)
--   - user_integration_identities.integration_uid  ON DELETE CASCADE  (automatic, 011_v0_14_0)
--   - incident_notifications.connection_uid        ON DELETE SET NULL (automatic — keeps audit history)
--   - escalation_policy_targets.target_uid          NOT a DB foreign key: it is a
--     polymorphic column shared by target_type in ('user','schedule','connection',
--     'all_admins'), so nothing enforces it. A 'connection' target pointing at an
--     opsgenie integration must be deleted explicitly here, or it becomes a
--     silently-dangling reference the moment the integration row is gone.

delete from escalation_policy_targets
where target_type = 'connection'
  and target_uid in (select uid from integrations where type = 'opsgenie');

--bun:split

delete from integrations where type = 'opsgenie';
