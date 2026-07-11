-- solidping v0.4.0 — drop the unused slug from escalation_policies and
-- on_call_schedules (spec 2026-07-11-10). SQLite cannot DROP COLUMN a column
-- that participates in a UNIQUE constraint, so each table is rebuilt without
-- slug (and without the (organization_uid, slug) unique constraint) — the
-- same rebuild technique 002_v0_2_0 used for user_tokens.
--
-- Both tables are FK-referenced parents (escalation_policy_steps,
-- on_call_schedule_users / _overrides cascade on delete; checks/incidents set
-- null), so foreign_keys is disabled around the rebuild to keep child rows
-- intact — the RENAME repoints their FK definitions to the rebuilt table. The
-- PRAGMA statements are isolated with --bun:split so they execute on the
-- migration connection in autocommit (a PRAGMA foreign_keys issued inside a
-- transaction is silently a no-op).
PRAGMA foreign_keys=OFF;

--bun:split

create table escalation_policies_new (
  uid                    text primary key,
  organization_uid       text not null references organizations(uid) on delete cascade,
  name                   text not null,
  description            text,
  repeat_max             integer not null default 0,
  repeat_after_seconds   integer,
  created_at             text not null default (datetime('now')),
  updated_at             text not null default (datetime('now')),
  deleted_at             text
);

insert into escalation_policies_new (
  uid, organization_uid, name, description, repeat_max, repeat_after_seconds,
  created_at, updated_at, deleted_at
)
select
  uid, organization_uid, name, description, repeat_max, repeat_after_seconds,
  created_at, updated_at, deleted_at
from escalation_policies;

drop table escalation_policies;
alter table escalation_policies_new rename to escalation_policies;

create index idx_escalation_policies_org on escalation_policies (organization_uid) where deleted_at is null;

--bun:split

create table on_call_schedules_new (
  uid               text primary key,
  organization_uid  text not null references organizations(uid) on delete cascade,
  name              text not null,
  description       text,
  timezone          text not null,
  rotation_type     text not null check (rotation_type in ('daily', 'weekly')),
  handoff_time      text not null,
  handoff_weekday   integer,
  start_at          text not null,
  ical_secret       text,
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text
);

insert into on_call_schedules_new (
  uid, organization_uid, name, description, timezone, rotation_type,
  handoff_time, handoff_weekday, start_at, ical_secret, created_at, updated_at, deleted_at
)
select
  uid, organization_uid, name, description, timezone, rotation_type,
  handoff_time, handoff_weekday, start_at, ical_secret, created_at, updated_at, deleted_at
from on_call_schedules;

drop table on_call_schedules;
alter table on_call_schedules_new rename to on_call_schedules;

create index idx_on_call_schedules_org on on_call_schedules (organization_uid) where deleted_at is null;

--bun:split

PRAGMA foreign_keys=ON;
