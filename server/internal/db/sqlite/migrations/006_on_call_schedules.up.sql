CREATE TABLE on_call_schedules (
  uid               text PRIMARY KEY,
  organization_uid  text NOT NULL REFERENCES organizations(uid) ON DELETE CASCADE,
  slug              text NOT NULL,
  name              text NOT NULL,
  description       text,
  timezone          text NOT NULL,
  rotation_type     text NOT NULL CHECK (rotation_type IN ('daily', 'weekly')),
  handoff_time      text NOT NULL,
  handoff_weekday   integer,
  start_at          text NOT NULL,
  ical_secret       text,
  created_at        text NOT NULL DEFAULT (datetime('now')),
  updated_at        text NOT NULL DEFAULT (datetime('now')),
  deleted_at        text,
  UNIQUE (organization_uid, slug)
);

CREATE INDEX idx_on_call_schedules_org
  ON on_call_schedules (organization_uid)
  WHERE deleted_at IS NULL;

CREATE TABLE on_call_schedule_users (
  uid           text PRIMARY KEY,
  schedule_uid  text NOT NULL REFERENCES on_call_schedules(uid) ON DELETE CASCADE,
  user_uid      text NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
  position      integer NOT NULL,
  created_at    text NOT NULL DEFAULT (datetime('now')),
  updated_at    text NOT NULL DEFAULT (datetime('now')),
  UNIQUE (schedule_uid, position),
  UNIQUE (schedule_uid, user_uid)
);

CREATE TABLE on_call_schedule_overrides (
  uid             text PRIMARY KEY,
  schedule_uid    text NOT NULL REFERENCES on_call_schedules(uid) ON DELETE CASCADE,
  user_uid        text NOT NULL REFERENCES users(uid) ON DELETE CASCADE,
  start_at        text NOT NULL,
  end_at          text NOT NULL,
  reason          text,
  created_by_uid  text REFERENCES users(uid) ON DELETE SET NULL,
  created_at      text NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_on_call_overrides_lookup
  ON on_call_schedule_overrides (schedule_uid, start_at, end_at);
