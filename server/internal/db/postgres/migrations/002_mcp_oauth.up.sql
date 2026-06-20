-- solidping — MCP OAuth 2.1 authorization server (spec 2026-06-20-03)
-- New tables for the embedded authorization server fronting the MCP resource:
-- registered clients, single-use authorization codes, and rotating refresh
-- grants. Does not amend the 001 baseline.

create table oauth_clients (
  uid           uuid primary key default gen_random_uuid(),
  client_id     text not null,
  secret_hash   text,
  client_name   text,
  redirect_uris jsonb,
  grant_types   jsonb,
  scopes        jsonb,
  is_public     boolean not null default true,
  created_at    timestamptz not null default now(),
  updated_at    timestamptz not null default now()
);

create unique index oauth_clients_client_id_idx on oauth_clients (client_id);

comment on table oauth_clients is 'OAuth 2.1 clients registered for the MCP resource (RFC 7591 dynamic registration or first-party).';
comment on column oauth_clients.secret_hash is 'Hashed client secret for confidential clients; NULL for public (native/loopback) clients.';
comment on column oauth_clients.is_public is 'True for public clients (PKCE + loopback redirects, no secret).';

create table oauth_auth_codes (
  uid                   uuid primary key default gen_random_uuid(),
  code                  text not null,
  client_id             text not null,
  user_uid              uuid not null references users(uid) on delete cascade,
  organization_uid      uuid not null references organizations(uid) on delete cascade,
  redirect_uri          text not null,
  scope                 text not null,
  resource              text not null,
  code_challenge        text not null,
  code_challenge_method text not null,
  expires_at            timestamptz not null,
  consumed_at           timestamptz,
  created_at            timestamptz not null default now()
);

create unique index oauth_auth_codes_code_idx on oauth_auth_codes (code);
create index oauth_auth_codes_expires_idx on oauth_auth_codes (expires_at);

comment on table oauth_auth_codes is 'Single-use, short-lived authorization codes bound to client/redirect/PKCE-challenge/resource/scope/user.';
comment on column oauth_auth_codes.consumed_at is 'Set on first redemption; a non-NULL value means the code was already used and a replay must be rejected.';

create table oauth_refresh_tokens (
  uid              uuid primary key default gen_random_uuid(),
  token            text not null,
  client_id        text not null,
  user_uid         uuid not null references users(uid) on delete cascade,
  organization_uid uuid not null references organizations(uid) on delete cascade,
  scope            text not null,
  resource         text not null,
  expires_at       timestamptz not null,
  revoked_at       timestamptz,
  created_at       timestamptz not null default now()
);

create unique index oauth_refresh_tokens_token_idx on oauth_refresh_tokens (token);
create index oauth_refresh_tokens_user_idx on oauth_refresh_tokens (user_uid) where revoked_at is null;

comment on table oauth_refresh_tokens is 'Rotating OAuth refresh grants for the MCP resource; revoked on rotation, logout, or PAT revoke.';
comment on column oauth_refresh_tokens.revoked_at is 'Set when the token is rotated out or revoked; a non-NULL value rejects further use.';
