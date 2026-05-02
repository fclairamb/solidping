-- On-call schedules. Resolves "who is on call right now" for a given org.
-- Schedules are the *who*; escalation policies (separate spec) are the
-- *when and through which medium*. Keeping the two distinct avoids the
-- confusing one-concept-two-places UI of legacy paging tools.

CREATE TABLE on_call_schedules (
  uid               uuid PRIMARY KEY,
  organization_uid  uuid NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
  slug              text NOT NULL,
  name              text NOT NULL,
  description       text NULL,
  timezone          text NOT NULL,
  rotation_type     text NOT NULL CHECK (rotation_type IN ('daily', 'weekly')),
  handoff_time      text NOT NULL,
  handoff_weekday   integer NULL,
  start_at          timestamptz NOT NULL,
  ical_secret       text NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  deleted_at        timestamptz NULL,
  CONSTRAINT on_call_schedules_org_slug_key UNIQUE (organization_uid, slug)
);

CREATE INDEX idx_on_call_schedules_org
  ON on_call_schedules (organization_uid)
  WHERE deleted_at IS NULL;

-- The ordered roster of users participating in a rotation. A user appears
-- at most once per schedule. For an "every other week" cadence, create a
-- separate schedule (deliberate v1 cut — see spec).
CREATE TABLE on_call_schedule_users (
  uid           uuid PRIMARY KEY,
  schedule_uid  uuid NOT NULL REFERENCES on_call_schedules(uid) ON DELETE CASCADE,
  user_uid      uuid NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
  position      integer NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT on_call_schedule_users_position_key UNIQUE (schedule_uid, position),
  CONSTRAINT on_call_schedule_users_user_key UNIQUE (schedule_uid, user_uid)
);

-- Time-bounded replacements ("Alice is on PTO 5/4–5/8, Bob covers").
-- Non-overlapping is enforced by app logic, not the DB — overlapping
-- overrides resolve to the most recently created one.
CREATE TABLE on_call_schedule_overrides (
  uid             uuid PRIMARY KEY,
  schedule_uid    uuid NOT NULL REFERENCES on_call_schedules(uid) ON DELETE CASCADE,
  user_uid        uuid NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
  start_at        timestamptz NOT NULL,
  end_at          timestamptz NOT NULL,
  reason          text NULL,
  created_by_uid  uuid NULL REFERENCES users(uid) ON DELETE SET NULL,
  created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_on_call_overrides_lookup
  ON on_call_schedule_overrides (schedule_uid, start_at, end_at);
