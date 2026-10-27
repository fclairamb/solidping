-- v0.43.0 — SQLite rollback (parity only; downs are never run in production).

-- SECTION: device-requester
alter table device_auth_requests drop column requester_user_agent;
alter table device_auth_requests drop column requester_ip;
