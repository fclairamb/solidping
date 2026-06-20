-- solidping — MCP OAuth 2.1 authorization server (spec 2026-06-20-03)
-- SQLite mirror of the PostgreSQL 002 migration. New tables for the embedded
-- authorization server fronting the MCP resource: registered clients,
-- single-use authorization codes, and rotating refresh grants.

create table oauth_clients (
  uid           text primary key,
  client_id     text not null,
  secret_hash   text,
  client_name   text,
  redirect_uris text,
  grant_types   text,
  scopes        text,
  is_public     integer not null default 1,
  created_at    text not null default (datetime('now')),
  updated_at    text not null default (datetime('now'))
);

create unique index oauth_clients_client_id_idx on oauth_clients (client_id);

create table oauth_auth_codes (
  uid                   text primary key,
  code                  text not null,
  client_id             text not null,
  user_uid              text not null references users(uid) on delete cascade,
  organization_uid      text not null references organizations(uid) on delete cascade,
  redirect_uri          text not null,
  scope                 text not null,
  resource              text not null,
  code_challenge        text not null,
  code_challenge_method text not null,
  expires_at            text not null,
  consumed_at           text,
  created_at            text not null default (datetime('now'))
);

create unique index oauth_auth_codes_code_idx on oauth_auth_codes (code);
create index oauth_auth_codes_expires_idx on oauth_auth_codes (expires_at);

create table oauth_refresh_tokens (
  uid              text primary key,
  token            text not null,
  client_id        text not null,
  user_uid         text not null references users(uid) on delete cascade,
  organization_uid text not null references organizations(uid) on delete cascade,
  scope            text not null,
  resource         text not null,
  expires_at       text not null,
  revoked_at       text,
  created_at       text not null default (datetime('now'))
);

create unique index oauth_refresh_tokens_token_idx on oauth_refresh_tokens (token);
create index oauth_refresh_tokens_user_idx on oauth_refresh_tokens (user_uid) where revoked_at is null;
