-- v0.39.0 — Postgres migration.
--
-- Frozen once tagged, like every migration: a later change goes in 030.
--
--   SECTION: org-plugin-types
--                              org_plugin_types, the plugin check types an
--                              org's private-location agents reported

-- ==========================================================================
-- SECTION: org-plugin-types (spec 2026-10-17-05 §18)
-- ==========================================================================
-- A private-location agent reports the plugin check types it loaded when it
-- connects. They are kept per org so the check form and the validation of
-- those types work while the agent is offline. One row per (org, type): the
-- latest report wins.
create table if not exists org_plugin_types (
  uid              uuid primary key default gen_random_uuid(),
  organization_uid uuid not null references organizations(uid) on delete cascade,
  check_type       text not null check (check_type ~ '^x-[a-z0-9]+(-[a-z0-9]+)*$'),
  manifest         jsonb not null,
  created_at       timestamptz not null default now(),
  updated_at       timestamptz not null default now()
);

--bun:split

create unique index if not exists org_plugin_types_org_type_idx
  on org_plugin_types (organization_uid, check_type);

--bun:split

comment on table org_plugin_types is
  'Plugin check types reported by an organization''s private-location agents (spec 2026-10-17-05). Read by the check form and validation while the agent is offline.';

--bun:split

comment on column org_plugin_types.organization_uid is
  'Organization whose agent reported the type. A type is only ever visible to this organization.';

--bun:split

comment on column org_plugin_types.check_type is
  'Plugin check type, always x- prefixed (e.g. x-gopher).';

--bun:split

comment on column org_plugin_types.manifest is
  'The reported type: plugin name, version, protocol, description, labels, periods and the raw JSON Schema of its config.';
