-- v0.43.0 — Postgres rollback (parity only; downs are never run in production).

-- SECTION: device-requester
alter table device_auth_requests drop column if exists requester_user_agent;
--bun:split
alter table device_auth_requests drop column if exists requester_ip;
