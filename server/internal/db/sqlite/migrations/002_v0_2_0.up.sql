-- solidping v0.2.0 — consolidated delta over the v0.1.0 baseline
-- SQLite mirror of the Postgres migration. Replaces incremental migrations
-- 002-009 with the net final schema changes. Do NOT run this file on a
-- database that already has 002-009 applied individually.

-- MCP OAuth 2.1 authorization server (spec 2026-06-20-03): registered clients
-- and rotating refresh grants. Authorization codes are NOT a dedicated table —
-- they're single-use, 60s-lived records in the generic state_entries store
-- (org-scoped, keyed "oauth_auth_code:<random>"; the org uid travels as a
-- prefix on the opaque code string itself, since the token endpoint that
-- redeems it has no other org context). See server/internal/oauth/service.go.

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

-- Discovery rework: check-centric grouped model (spec 2026-06-21-00). Clean
-- break: discovered_hosts is dropped (results are regenerable by re-scan).

drop table if exists discovered_hosts;

create table discovered_checks (
  uid                   text primary key,
  organization_uid      text not null references organizations(uid),
  job_uid               text not null,
  source                text not null,
  group_key             text not null,
  group_label           text not null,
  name                  text not null,
  slug                  text not null,
  type                  text not null,
  config                text not null default '{}',
  metadata              text,
  promoted_to_check_uid text references checks(uid),
  discovered_at         text not null default (datetime('now')),
  created_at            text not null default (datetime('now')),
  updated_at            text not null default (datetime('now')),
  deleted_at            text
);

-- One row per (group, slug) per source while active & unpromoted → upsert key.
create unique index idx_discovered_checks_identity_active
  on discovered_checks (organization_uid, source, group_key, slug)
  where deleted_at is null and promoted_to_check_uid is null;

-- List/group by scan, and source-filter.
create index idx_discovered_checks_job        on discovered_checks (job_uid)                  where deleted_at is null;
create index idx_discovered_checks_org_source on discovered_checks (organization_uid, source) where deleted_at is null;

-- Status pages: explicit history period enum (spec 2026-06-30-03). Mirrors the
-- badge uptime-bar vocabulary so status pages can render a 24h hourly view.
-- history_days is kept populated for one release for backward-compat.

alter table status_pages add column history_period text not null default '90d'; -- 24h (hourly) | 7d | 30d | 90d

-- Adaptive recovery: flapping backoff with a cap (spec 2026-06-30-07). Replaces
-- the dead `max_adaptive_increase` knob with a real time-based flapping model:
-- when a check flaps (repeated outages over a short horizon), require
-- progressively longer stability before auto-resolving each successive
-- incident — bounded by a cap and reset after a calm window.
--
-- Off-by-default-equivalent: flap_backoff_factor=1 or flapping_window_seconds=0
-- reproduces the constant recovery_period_seconds behaviour.
-- (DROP COLUMN requires SQLite >= 3.35; the bundled engine supports it.)

alter table checks add column flapping_window_seconds integer not null default 21600; -- 6h rolling flap window. 0 = adaptive recovery off
alter table checks add column flap_backoff_factor integer not null default 2; -- Per-flap recovery multiplier. 1 = off
alter table checks add column max_recovery_multiplier integer not null default 8; -- Cap on required-recovery vs base recovery period
alter table checks add column flap_count integer not null default 0; -- Outages in the rolling window; written only on incident open/reopen
alter table checks add column last_outage_at text; -- Wall-clock of most recent outage onset; gates the window reset. NULL until first outage

-- The old `max_adaptive_increase` was dead (consumed by nothing); dropped
-- rather than backfilled.
alter table checks drop column max_adaptive_increase;

-- Cost-aware, plan-weighted check scheduling (spec 2026-06-30-09) plus its two
-- follow-ups: per-job scheduling-delay EWMA (spec 2026-06-30-09 extension) and
-- fast/slow check lanes (spec 2026-07-01-03). Net final shape after
-- migrations 006-009:
--   * cost_ewma_ms          — EWMA of execution duration, timeouts pinned to
--                             the ceiling. Drives slow-lane classification and
--                             the cost-aware execution timeout.
--   * plan_weight           — denormalized plan tier (0 = free, higher = paid).
--   * effective_scheduled_at — scheduled_at + cost_ewma_ms×2 (clamped to 60s)
--                             − tier_credit. Claim ORDER BY key only; the
--                             claim gate stays on the real scheduled_at.
--   * delay_ewma_ms         — pure telemetry (probe start − scheduled_at);
--                             never steers claim ordering (007's feedback-loop
--                             bug from folding delay into the ordering offset
--                             was fixed by 008 before this consolidation).
--   * lane                  — 0 = fast, 1 = slow, classified from cost_ewma_ms
--                             with hysteresis in the post-exec write. Two
--                             per-lane partial indexes back the claim's
--                             lane-filtered SELECTs.
--
-- Off-by-default-equivalent: penalty/credit = 0 and the caps default to the
-- pool size, so a fresh DB reproduces pure-FIFO behaviour.

alter table check_jobs add column cost_ewma_ms real not null default 0; -- EWMA of execution duration in ms; timeouts pinned to ceiling. 0 until first run
alter table check_jobs add column plan_weight integer not null default 0; -- Plan tier: 0 = free, higher = more protected. Refreshed on entitlement change + reconcile
alter table check_jobs add column effective_scheduled_at text; -- scheduled_at + cost_ewma_ms×2 (clamped to 60s) − tier_credit. Claim ORDER BY key only
alter table check_jobs add column delay_ewma_ms real not null default 0; -- EWMA of (probe start − scheduled_at) in ms, floored at 0. Pure telemetry; never steers claim ordering
alter table check_jobs add column lane smallint not null default 0; -- 0 = fast, 1 = slow — classified from cost_ewma_ms with hysteresis in the post-exec release

create index if not exists idx_check_jobs_claim_fast
    on check_jobs (effective_scheduled_at) where lane = 0;
create index if not exists idx_check_jobs_claim_slow
    on check_jobs (effective_scheduled_at) where lane = 1;
