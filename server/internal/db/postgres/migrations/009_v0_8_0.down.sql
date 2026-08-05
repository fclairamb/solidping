-- Teardown/parity only — never run in production. Reverses 009_v0_8_0.up.sql.

-- reverse per-status-page availability color thresholds (spec 2026-08-03-01)
alter table status_pages drop column if exists settings;

-- reverse entity slug max length raised to 100 chars (spec 2026-08-04-01)
alter table check_groups drop constraint if exists check_groups_slug_check;
alter table check_groups add constraint check_groups_slug_check
  check (slug ~ '^[a-z][a-z0-9-]{2,39}$');

alter table checks drop constraint if exists checks_slug_check;
alter table checks add constraint checks_slug_check
  check (slug is null or slug ~ '^[a-z][a-z0-9-]{2,49}$');

alter table status_pages drop constraint if exists status_pages_slug_check;
alter table status_pages add constraint status_pages_slug_check
  check (slug ~ '^[a-z][a-z0-9-]{2,39}$');

alter table status_page_sections drop constraint if exists status_page_sections_slug_check;
alter table status_page_sections add constraint status_page_sections_slug_check
  check (slug ~ '^[a-z][a-z0-9-]{2,39}$');
