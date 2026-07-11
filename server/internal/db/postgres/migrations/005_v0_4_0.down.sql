-- Reverse of 005_v0_4_0.up.sql: re-add the slug columns and their per-org
-- unique constraints. Teardown/parity only — never run in production. The
-- original slug values are gone, so backfill from uid to satisfy NOT NULL and
-- the (organization_uid, slug) uniqueness.
alter table escalation_policies add column slug text;
update escalation_policies set slug = uid::text where slug is null;
alter table escalation_policies alter column slug set not null;
alter table escalation_policies
  add constraint escalation_policies_organization_uid_slug_key unique (organization_uid, slug);

alter table on_call_schedules add column slug text;
update on_call_schedules set slug = uid::text where slug is null;
alter table on_call_schedules alter column slug set not null;
alter table on_call_schedules
  add constraint on_call_schedules_organization_uid_slug_key unique (organization_uid, slug);
