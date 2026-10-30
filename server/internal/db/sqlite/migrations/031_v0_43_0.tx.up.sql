-- v0.43.0 — SQLite twin of the Postgres 031_v0_43_0 migration.
--
-- Frozen once tagged, like every migration: a later change goes in 032.

-- SECTION: device-requester (spec 2026-10-27-01)
-- device_auth_requests.requester_ip: client IP that opened the request
-- (trusted-proxy resolution); NULL for rows opened before v0.43.0.
alter table device_auth_requests add column requester_ip text;
-- device_auth_requests.requester_user_agent: User-Agent of that client,
-- truncated; NULL when absent.
alter table device_auth_requests add column requester_user_agent text;

-- SECTION: subscriber-confirm-expiry (spec 2026-10-28-02)
-- status_page_subscriber.confirm_sent_at: when the current confirm token was
-- issued; the token expires 7 days later and unconfirmed rows past that are
-- reaped by the state-cleanup job. NULL for older rows (created_at applies).
alter table status_page_subscriber add column confirm_sent_at text;
