ALTER TABLE check_groups DROP COLUMN IF EXISTS escalation_policy_uid;
ALTER TABLE checks       DROP COLUMN IF EXISTS escalation_policy_uid;

DROP TABLE IF EXISTS escalation_policy_targets;
DROP TABLE IF EXISTS escalation_policy_steps;
DROP TABLE IF EXISTS escalation_policies;
