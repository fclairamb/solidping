-- SECTION: localized-generated-text
alter table status_updates drop column if exists body_i18n;
alter table status_updates drop column if exists title_i18n;
alter table incident_publications drop column if exists public_title_i18n;
alter table incidents drop column if exists title_i18n;
