-- Teardown/parity only — never run in production. Reverses 012_org_profile.up.sql.

drop index if exists organization_previous_slugs_organization_idx;
drop index if exists organization_previous_slugs_slug_idx;
drop table if exists organization_previous_slugs;

alter table organizations drop column if exists logo_file_uid;
alter table organizations drop column if exists logo_url;
