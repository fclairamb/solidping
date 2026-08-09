-- Teardown/parity only — never run in production. Reverses 012_org_profile.up.sql.

drop index if exists organization_previous_slugs_organization_idx;

--bun:split

drop index if exists organization_previous_slugs_slug_idx;

--bun:split

drop table if exists organization_previous_slugs;

--bun:split

alter table organizations drop column logo_file_uid;

--bun:split

alter table organizations drop column logo_url;
