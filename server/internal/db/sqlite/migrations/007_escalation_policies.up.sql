CREATE TABLE escalation_policies (
  uid                  text PRIMARY KEY,
  organization_uid     text NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
  slug                 text NOT NULL,
  name                 text NOT NULL,
  description          text,
  repeat_max           integer NOT NULL DEFAULT 0,
  repeat_after_minutes integer,
  created_at           text NOT NULL DEFAULT (datetime('now')),
  updated_at           text NOT NULL DEFAULT (datetime('now')),
  deleted_at           text,
  UNIQUE (organization_uid, slug)
);

CREATE INDEX idx_escalation_policies_org
  ON escalation_policies (organization_uid)
  WHERE deleted_at IS NULL;

CREATE TABLE escalation_policy_steps (
  uid           text PRIMARY KEY,
  policy_uid    text NOT NULL REFERENCES escalation_policies(uid) ON DELETE CASCADE,
  position      integer NOT NULL,
  delay_minutes integer NOT NULL DEFAULT 0,
  created_at    text NOT NULL DEFAULT (datetime('now')),
  updated_at    text NOT NULL DEFAULT (datetime('now')),
  UNIQUE (policy_uid, position)
);

CREATE TABLE escalation_policy_targets (
  uid         text PRIMARY KEY,
  step_uid    text NOT NULL REFERENCES escalation_policy_steps(uid) ON DELETE CASCADE,
  target_type text NOT NULL CHECK (target_type IN ('user', 'schedule', 'connection', 'all_admins')),
  target_uid  text,
  position    integer NOT NULL DEFAULT 0
);

CREATE INDEX idx_escalation_targets_step
  ON escalation_policy_targets (step_uid);

ALTER TABLE checks       ADD COLUMN escalation_policy_uid text REFERENCES escalation_policies(uid) ON DELETE SET NULL;
ALTER TABLE check_groups ADD COLUMN escalation_policy_uid text REFERENCES escalation_policies(uid) ON DELETE SET NULL;
