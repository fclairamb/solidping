ALTER TABLE escalation_policy_steps DROP COLUMN severity_uid;
DROP INDEX IF EXISTS severities_org_default_alive_idx;
DROP INDEX IF EXISTS severities_org_slug_alive_idx;
DROP TABLE IF EXISTS severities;
