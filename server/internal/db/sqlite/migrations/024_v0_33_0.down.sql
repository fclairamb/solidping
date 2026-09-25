-- Teardown/parity half of the consolidated v0.33.0 SQLite migration — never
-- run in production. Sections appear in the EXACT REVERSE order of
-- 024_v0_33_0.up.sql.

-- ==========================================================================
-- SECTION: hash-user-tokens
--
-- ⚠️ A DOWNGRADE SIGNS EVERYONE OUT. The stored hashes cannot be turned back
-- into tokens, so every user_tokens row is deleted: all sessions, PATs and
-- OAuth refresh grants are revoked. The table is rebuilt in its v0.32 shape
-- (plaintext `token`, no token_hash), which works whether or not
-- db.HashPlaintextUserTokens already dropped `token` (SQLite has no
-- ADD COLUMN IF NOT EXISTS).
-- ==========================================================================

delete from user_tokens;

--bun:split

drop index if exists user_tokens_token_hash_idx;

--bun:split

create table user_tokens_v0_32 (
  uid               text primary key,
  user_uid          text not null references users(uid) on delete cascade, -- Token owner
  organization_uid  text references organizations(uid) on delete cascade, -- Organization scope for PAT tokens. NULL for global refresh tokens
  token             text not null, -- Hashed token value
  type              text not null check (type in ('pat', 'refresh', 'oauth_refresh')), -- Token type: pat, session refresh, or rotating OAuth refresh grant (client_id/scope/resource in properties)
  properties        text, -- Token metadata (e.g., name, scopes, IP restrictions)
  expires_at        text, -- Expiration timestamp. NULL means never expires
  last_active_at    text, -- Last time this token was used for authentication
  created_at        text not null default (datetime('now')),
  updated_at        text not null default (datetime('now')),
  deleted_at        text
);

--bun:split

drop table user_tokens;

--bun:split

alter table user_tokens_v0_32 rename to user_tokens;

--bun:split

create unique index if not exists user_tokens_token_idx on user_tokens (token) where deleted_at is null;

--bun:split

create index if not exists user_tokens_user_uid_idx on user_tokens (user_uid) where deleted_at is null;

--bun:split

create index if not exists user_tokens_expires_at_idx on user_tokens (expires_at) where deleted_at is null and expires_at is not null;

--bun:split

-- ==========================================================================
-- SECTION: auth-handoff-codes
--
-- Outstanding handoff codes are dropped; a login mid-handoff has to start
-- again.
-- ==========================================================================

drop table if exists auth_handoff_codes;

--bun:split

-- ==========================================================================
-- SECTION: multi-region-quorum
--
-- The per-region readings and the quorum setting go; every check falls back
-- to the per-result state machine, which is what the previous schema ran.
-- ==========================================================================

drop table if exists check_region_states;

--bun:split

alter table checks drop column fail_quorum;

--bun:split

-- ==========================================================================
-- SECTION: drop-degraded-dry-run
--
-- The column comes back empty and the sweep index regains its 023 predicate.
-- ==========================================================================

drop index if exists idx_checks_degraded_eval;

--bun:split

create index if not exists idx_checks_degraded_eval
  on checks (degraded_evaluated_at)
  where deleted_at is null and enabled;

--bun:split

alter table checks add column degraded_would_fire_at text;

--bun:split

-- ==========================================================================
-- SECTION: degraded-incident-kind
--
-- Nothing to undo on SQLite.
-- ==========================================================================

-- ==========================================================================
-- SECTION: auto-region-placement
--
-- The placement columns go; every check keeps its regions (the placement
-- the scheduler last wrote), which the previous schema reads as pinned.
-- ==========================================================================

alter table checks drop column region_pool;

--bun:split

alter table checks drop column region_count;

--bun:split

alter table checks drop column placement;

--bun:split

-- ==========================================================================
-- SECTION: passive-checks-no-regions
--
-- Nothing to undo: the up section is a data normalization (passive checks
-- lose their regions and keep one NULL-region job), which the previous schema
-- accepts as is.
-- ==========================================================================

-- ==========================================================================
-- SECTION: check-freshness
-- ==========================================================================

drop index if exists idx_checks_freshness;

--bun:split

update checks set status = 1 where status = 10;

--bun:split

alter table checks drop column last_result_at;
