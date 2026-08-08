-- solidping v0.9.0 — consolidated release migration.
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
-- the minted PAT here and the first successful poll deletes the row, so the
-- token is delivered exactly once. A background purge sweeps rows whose human
-- never showed up.

create table device_auth_requests (
  uid               uuid primary key default gen_random_uuid(),
  device_code       text not null,
  user_code         text not null,
  client_name       text not null,
  status            text not null default 'pending',
  organization_uid  uuid references organizations(uid) on delete cascade,
  user_uid          uuid references users(uid) on delete cascade,
  token_uid         uuid,
  token_value       text,
  last_polled_at    timestamptz,
  expires_at        timestamptz not null,
  created_at        timestamptz not null default now(),
  updated_at        timestamptz not null default now(),
  constraint device_auth_requests_status_check
    check (status in ('pending', 'approved', 'denied'))
);

create unique index device_auth_requests_device_code_idx on device_auth_requests (device_code);
create unique index device_auth_requests_user_code_idx on device_auth_requests (user_code);
create index device_auth_requests_expires_at_idx on device_auth_requests (expires_at);

comment on table device_auth_requests is
  'Pending RFC 8628 device-authorization requests (sp auth login). Short-lived and single-use: the first successful poll after approval deletes the row.';
comment on column device_auth_requests.device_code is
  'The requesting client''s secret (32 random bytes, hex). This is the real capability — never shown in a browser.';
comment on column device_auth_requests.user_code is
  'Canonical (uppercase, dashless) short code the human types on the consent page. Brute-forceable surface — the consent lookup is rate limited.';
comment on column device_auth_requests.organization_uid is
  'Organization selected by the approving user at consent time. The minted PAT is scoped to it — NOT implicitly to the approver''s session org.';
comment on column device_auth_requests.token_value is
  'The minted PAT, stashed between approval and the client''s next poll, then deleted with the row. Same at-rest exposure as user_tokens.token.';
