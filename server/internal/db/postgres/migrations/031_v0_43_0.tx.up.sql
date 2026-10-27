-- v0.43.0 — Postgres migration.
--
-- Frozen once tagged, like every migration: a later change goes in 032.
--
--   SECTION: device-requester   device_auth_requests.requester_ip / requester_user_agent

-- ==========================================================================
-- SECTION: device-requester (spec 2026-10-27-01)
-- ==========================================================================
-- Who opened a device-authorization request, shown on the consent page so a
-- member can tell a request they started from one someone sent them.
alter table device_auth_requests add column if not exists requester_ip text;

--bun:split

alter table device_auth_requests add column if not exists requester_user_agent text;

--bun:split

comment on column device_auth_requests.requester_ip is 'Client IP that opened the request, resolved with the trusted-proxy rules of the rate limiter. NULL for rows opened before v0.43.0.';

--bun:split

comment on column device_auth_requests.requester_user_agent is 'User-Agent header of the client that opened the request, truncated. NULL when absent.';
