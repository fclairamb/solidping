-- Teardown/parity only — never run in production. Reverses 009_v0_8_0.up.sql.

-- reverse per-status-page availability color thresholds (spec 2026-08-03-01)
alter table status_pages drop column settings;
