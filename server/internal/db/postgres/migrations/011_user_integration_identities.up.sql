-- Per-integration member identity mapping (spec 2026-08-12-03).
--
-- Answers "who is this org member on this integration instance" — e.g. which
-- Slack user id to render as <@U123ABC> when an alert lands in a channel.
--
-- Deliberately NOT user_contacts: contacts are "how to page me" and carry
-- verification state; identities are "who I am there" and are used for
-- mentions and attribution only. Nothing in the paging path reads this table,
-- so a wrong row can annoy but can never misdirect a page.
--
-- Scoped per INTEGRATION rather than per integration type, because an org can
-- connect several Slack workspaces and the same human has a different user id
-- in each. The two unique indexes make the mapping a bijection inside one
-- integration: a member has at most one identity, and an external id belongs
-- to at most one member.
--
-- Scratch migration for the in-flight release cycle; it is folded into the
-- consolidated release migration at release time (see
-- wiki/conventions/database.md).

create table if not exists user_integration_identities (
  uid              uuid primary key default gen_random_uuid(),
  organization_uid uuid not null references organizations(uid) on delete cascade,
  integration_uid  uuid not null references integrations(uid) on delete cascade,
  user_uid         uuid not null references users(uid) on delete cascade,
  external_id      text not null,
  display_name     text not null default '',
  source           text not null default 'auto',
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now(),
  constraint user_integration_identities_source_check
    check (source in ('auto', 'manual'))
);

create unique index if not exists user_integration_identities_integration_user_idx
  on user_integration_identities (integration_uid, user_uid);
create unique index if not exists user_integration_identities_integration_external_idx
  on user_integration_identities (integration_uid, external_id);
create index if not exists user_integration_identities_organization_idx
  on user_integration_identities (organization_uid);

comment on table user_integration_identities is
  'Maps an org member to their provider-side identity on ONE integration instance (e.g. their Slack user id on a specific workspace). Used for mentions and attribution only — never for paging.';
comment on column user_integration_identities.integration_uid is
  'The integration instance this identity is valid on. An org with two Slack workspaces has one row per workspace.';
comment on column user_integration_identities.user_uid is
  'The SolidPing user this identity belongs to.';
comment on column user_integration_identities.external_id is
  'Provider-side identifier used to address the person in a message (Slack user id, rendered as <@id>).';
comment on column user_integration_identities.display_name is
  'Provider-side display name captured at match time. Cosmetic: shown in the admin UI and used for the plain-text fallback.';
comment on column user_integration_identities.source is
  'How the mapping was established: auto (email auto-match) or manual (an admin picked it). A re-sync never overwrites a manual row.';
