-- solidping v0.10.0 — consolidated release migration.
-- SQLite mirror of the Postgres migration.
--
-- Multiple features share this one release migration; each contributes a
-- clearly separated block below. Append new blocks at the end.

-- ---------------------------------------------------------------------------
-- OAuth 2.0 Device Authorization Grant, RFC 8628 (spec 2026-08-08-02)
-- ---------------------------------------------------------------------------
-- Pending device-authorization requests for `sp auth login`. The CLI opens a
-- request, shows the short user_code, and polls the token endpoint with the
-- device_code until a logged-in human approves it in the dashboard.
--
-- Rows are short-lived (15 min) and single-use: the approving request stashes
-- the minted PAT here (token_value) and the first successful poll deletes the
-- row, so the token is delivered exactly once. device_code is the real
-- capability; user_code is the brute-forceable surface and the consent lookup
-- is rate limited.

create table device_auth_requests (
  uid               text primary key,
  device_code       text not null,
  user_code         text not null,
  client_name       text not null,
  status            text not null default 'pending',
  organization_uid  text references organizations(uid) on delete cascade, -- Org selected at consent time; the minted PAT is scoped to it
  user_uid          text references users(uid) on delete cascade, -- Approving user
  token_uid         text,
  token_value       text, -- Minted PAT, deleted with the row on first delivery
  last_polled_at    text,
  expires_at        text not null,
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  constraint device_auth_requests_status_check
    check (status in ('pending', 'approved', 'denied'))
);

create unique index device_auth_requests_device_code_idx on device_auth_requests (device_code);
create unique index device_auth_requests_user_code_idx on device_auth_requests (user_code);
create index device_auth_requests_expires_at_idx on device_auth_requests (expires_at);
