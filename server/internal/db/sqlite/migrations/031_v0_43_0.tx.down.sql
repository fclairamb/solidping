-- v0.43.0 — SQLite rollback (parity only; downs are never run in production).

-- SECTION: auto-personal-org
drop index if exists organizations_auto_personal_owner_idx;
alter table organizations drop column auto_personal_owner_uid;

-- SECTION: subscriber-confirm-expiry
alter table status_page_subscriber drop column confirm_sent_at;

-- SECTION: device-requester
alter table device_auth_requests drop column requester_user_agent;
alter table device_auth_requests drop column requester_ip;
