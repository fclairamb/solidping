-- v0.52.0 — SQLite rollback (parity only; downs are never run in production).

-- SECTION: localized-generated-text
alter table status_updates drop column body_i18n;
alter table status_updates drop column title_i18n;
alter table incident_publications drop column public_title_i18n;
alter table incidents drop column title_i18n;
