-- Teardown/parity only — never run in production. Reverses 010_v0_10_0.up.sql.

-- reverse OAuth 2.0 device authorization grant (spec 2026-08-08-02)
drop table if exists device_auth_requests;
