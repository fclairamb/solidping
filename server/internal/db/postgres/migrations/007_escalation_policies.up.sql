-- Escalation policies. Reusable orchestration over notification connections,
-- on-call schedules, and admin fan-out. Distinct from check_connections,
-- which remain the per-check broadcast layer.

CREATE TABLE escalation_policies (
  uid                  uuid PRIMARY KEY,
  organization_uid     uuid NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
  slug                 text NOT NULL,
  name                 text NOT NULL,
  description          text NULL,
  repeat_max           integer NOT NULL DEFAULT 0,
  repeat_after_minutes integer NULL,
  created_at           timestamptz NOT NULL DEFAULT now(),
  updated_at           timestamptz NOT NULL DEFAULT now(),
  deleted_at           timestamptz NULL,
  CONSTRAINT escalation_policies_org_slug_key UNIQUE (organization_uid, slug)
);

CREATE INDEX idx_escalation_policies_org
  ON escalation_policies (organization_uid)
  WHERE deleted_at IS NULL;

CREATE TABLE escalation_policy_steps (
  uid           uuid PRIMARY KEY,
  policy_uid    uuid NOT NULL REFERENCES escalation_policies(uid) ON DELETE CASCADE,
  position      integer NOT NULL,
  delay_minutes integer NOT NULL DEFAULT 0,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT escalation_policy_steps_position_key UNIQUE (policy_uid, position)
);

CREATE TABLE escalation_policy_targets (
  uid         uuid PRIMARY KEY,
  step_uid    uuid NOT NULL REFERENCES escalation_policy_steps(uid) ON DELETE CASCADE,
  target_type text NOT NULL CHECK (target_type IN ('user', 'schedule', 'connection', 'all_admins')),
  target_uid  uuid NULL,
  position    integer NOT NULL DEFAULT 0
);

CREATE INDEX idx_escalation_targets_step
  ON escalation_policy_targets (step_uid);

ALTER TABLE checks       ADD COLUMN escalation_policy_uid uuid NULL REFERENCES escalation_policies(uid) ON DELETE SET NULL;
ALTER TABLE check_groups ADD COLUMN escalation_policy_uid uuid NULL REFERENCES escalation_policies(uid) ON DELETE SET NULL;
