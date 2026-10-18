-- v0.39.0 — SQLite twin of the Postgres 029_v0_39_0 migration.
--
-- Frozen once tagged, like every migration: a later change goes in 030.

-- SECTION: org-plugin-types (spec 2026-10-17-05 §18)
-- org_plugin_types: plugin check types reported by an organization's
-- private-location agents, kept so the check form and validation work while
-- the agent is offline. One row per (org, type): the latest report wins.
create table if not exists org_plugin_types (
  uid              text primary key,
  organization_uid text not null references organizations(uid) on delete cascade, -- Organization whose agent reported the type; only ever visible to it
  check_type       text not null check (length(check_type) > 2 and substr(check_type, 1, 2) = 'x-'), -- Plugin check type, always x- prefixed
  manifest         text not null, -- The reported type as JSON: plugin, version, protocol, description, labels, periods, raw schema
  created_at       text not null default (datetime('now')),
  updated_at       text not null default (datetime('now'))
);

--bun:split

create unique index if not exists org_plugin_types_org_type_idx
  on org_plugin_types (organization_uid, check_type);
