-- v0.43.0 — SQLite rollback (parity only; downs are never run in production).

-- SECTION: subscriber-confirm-expiry
alter table status_page_subscriber drop column confirm_sent_at;

-- SECTION: device-requester
alter table device_auth_requests drop column requester_user_agent;
alter table device_auth_requests drop column requester_ip;
