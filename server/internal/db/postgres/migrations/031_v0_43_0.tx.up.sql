-- v0.43.0 — Postgres migration.
--
-- Frozen once tagged, like every migration: a later change goes in 032.
--
--   SECTION: device-requester   device_auth_requests.requester_ip / requester_user_agent
--   SECTION: subscriber-confirm-expiry   status_page_subscriber.confirm_sent_at
--   SECTION: auto-personal-org   organizations.auto_personal_owner_uid

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

--bun:split

-- ==========================================================================
-- SECTION: subscriber-confirm-expiry (spec 2026-10-28-02)
-- ==========================================================================
-- When the current confirm token was issued, so a confirm link expires and
-- the state-cleanup job can reap unconfirmed rows nobody came back for.
alter table status_page_subscriber add column if not exists confirm_sent_at timestamptz;

--bun:split

comment on column status_page_subscriber.confirm_sent_at is 'When the current confirm token was issued (subscribe or re-subscribe). The token expires 7 days later; unconfirmed rows past that are hard-deleted by the state-cleanup job. NULL for rows created before v0.43.0, which fall back to created_at.';

--bun:split

-- ==========================================================================
-- SECTION: auto-personal-org (spec 2026-10-30-04)
-- ==========================================================================
-- The user an organization was created for automatically at their first
-- sign-in. Marks the org as an auto-created personal org (the abandoned-org
-- cleanup and the colleague hint target it), and the partial unique index
-- makes the creation race-safe: one live personal org per user.
alter table organizations add column if not exists auto_personal_owner_uid uuid references users(uid) on delete set null;

--bun:split

create unique index if not exists organizations_auto_personal_owner_idx on organizations (auto_personal_owner_uid) where deleted_at is null and auto_personal_owner_uid is not null;

--bun:split

comment on column organizations.auto_personal_owner_uid is 'User this organization was created for automatically at their first sign-in (auth.auto_personal_org). NULL for organizations created by hand. Abandoned ones (no check, one member) are deleted after 30 days.';
