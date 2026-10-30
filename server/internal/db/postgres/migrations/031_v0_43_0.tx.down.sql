-- v0.43.0 — Postgres rollback (parity only; downs are never run in production).

-- SECTION: auto-personal-org
drop index if exists organizations_auto_personal_owner_idx;
--bun:split
alter table organizations drop column if exists auto_personal_owner_uid;
--bun:split

-- SECTION: subscriber-confirm-expiry
alter table status_page_subscriber drop column if exists confirm_sent_at;
--bun:split

-- SECTION: device-requester
alter table device_auth_requests drop column if exists requester_user_agent;
--bun:split
alter table device_auth_requests drop column if exists requester_ip;
