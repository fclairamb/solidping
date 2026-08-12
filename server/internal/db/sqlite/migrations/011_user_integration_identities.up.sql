-- Per-integration member identity mapping (spec 2026-08-12-03).
-- See the postgres twin for the full rationale.
--
-- user_integration_identities: who an org member is on ONE integration
-- instance (e.g. their Slack user id on a specific workspace). Used for
-- mentions and attribution only — the paging path never reads it.

create table if not exists user_integration_identities (
  uid              text primary key,
  organization_uid text not null references organizations(uid) on delete cascade,
  integration_uid  text not null references integrations(uid) on delete cascade, -- The integration instance this identity is valid on (one row per workspace)
  user_uid         text not null references users(uid) on delete cascade, -- The SolidPing user this identity belongs to
  external_id      text not null, -- Provider-side id used to address the person in a message (Slack user id, rendered as <@id>)
  display_name     text not null default '', -- Provider-side display name captured at match time; cosmetic + plain-text fallback
  source           text not null default 'auto', -- auto (email auto-match) or manual (an admin picked it); a re-sync never overwrites manual
  created_at       text not null default (datetime('now')),
  updated_at       text not null default (datetime('now')),
  check (source in ('auto', 'manual'))
);

--bun:split

create unique index if not exists user_integration_identities_integration_user_idx
  on user_integration_identities (integration_uid, user_uid);

--bun:split

create unique index if not exists user_integration_identities_integration_external_idx
  on user_integration_identities (integration_uid, external_id);

--bun:split

create index if not exists user_integration_identities_organization_idx
  on user_integration_identities (organization_uid);
