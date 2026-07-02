-- solidping v0.2.0 — consolidated delta over the v0.1.0 baseline
-- Replaces incremental migrations 002-009 with the net final schema changes.
-- Do NOT run this file on a database that already has 002-009 applied individually.

-- MCP OAuth 2.1 authorization server (spec 2026-06-20-03): registered clients
-- and rotating refresh grants. Authorization codes are NOT a dedicated table —
-- they're single-use, 60s-lived records in the generic state_entries store
-- (org-scoped, keyed "oauth_auth_code:<random>"; the org uid travels as a
-- prefix on the opaque code string itself, since the token endpoint that
-- redeems it has no other org context). See server/internal/oauth/service.go.

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

-- Discovery rework: check-centric grouped model (spec 2026-06-21-00). Discovery
-- is no longer host-centric. The stored unit is a *suggested check*; rows are
-- grouped for display by group_key (an IP, container ID, workload uid...).
-- Clean break: discovered_hosts is dropped (results are regenerable by re-scan).

drop table if exists discovered_hosts;

create table discovered_checks (
  uid                   uuid        primary key default gen_random_uuid(),
  organization_uid      uuid        not null references organizations(uid),
  job_uid               uuid        not null,
  source                text        not null,
  group_key             text        not null,
  group_label           text        not null,
  name                  text        not null,
  slug                  text        not null,
  type                  text        not null,
  config                jsonb       not null default '{}',
  metadata              jsonb,
  promoted_to_check_uid uuid        references checks(uid),
  discovered_at         timestamptz not null default now(),
  created_at            timestamptz not null default now(),
  updated_at            timestamptz not null default now(),
  deleted_at            timestamptz
);

-- One row per (group, slug) per source while active & unpromoted → upsert key.
create unique index idx_discovered_checks_identity_active
  on discovered_checks (organization_uid, source, group_key, slug)
  where deleted_at is null and promoted_to_check_uid is null;

-- List/group by scan, and source-filter.
create index idx_discovered_checks_job        on discovered_checks (job_uid)                  where deleted_at is null;
create index idx_discovered_checks_org_source on discovered_checks (organization_uid, source) where deleted_at is null;

comment on table discovered_checks is 'Suggested checks produced by discovery scans, grouped for display by group_key. Ephemeral scratch data — regenerable by re-scanning.';
comment on column discovered_checks.group_key is 'Stable grouping identity (IP, container ID, workload uid). A render-time GROUP BY, not a second table.';
comment on column discovered_checks.metadata is 'Denormalized group-display hints (identical across a group''s rows), written by the suggester.';

-- Status pages: explicit history period enum (spec 2026-06-30-03). Mirrors the
-- badge uptime-bar vocabulary so status pages can render a 24h hourly view.
-- history_days is kept populated for one release for backward-compat.

alter table status_pages add column history_period text not null default '90d';

comment on column status_pages.history_period is 'History window the page renders: 24h (hourly) | 7d | 30d | 90d. Source of truth for bucketing; history_days kept for back-compat.';

-- Adaptive recovery: flapping backoff with a cap (spec 2026-06-30-07). Replaces
-- the dead `max_adaptive_increase` knob with a real time-based flapping model:
-- when a check flaps (repeated outages over a short horizon), require
-- progressively longer stability before auto-resolving each successive
-- incident — bounded by a cap and reset after a calm window.
--
-- Off-by-default-equivalent: flap_backoff_factor=1 or flapping_window_seconds=0
-- reproduces the constant recovery_period_seconds behaviour.

alter table checks add column flapping_window_seconds integer not null default 21600; -- 6h
alter table checks add column flap_backoff_factor integer not null default 2;
alter table checks add column max_recovery_multiplier integer not null default 8;
alter table checks add column flap_count integer not null default 0;
alter table checks add column last_outage_at timestamptz;

-- The old `max_adaptive_increase` was dead (consumed by nothing); dropped
-- rather than backfilled.
alter table checks drop column max_adaptive_increase;

comment on column checks.flapping_window_seconds is 'Rolling window over which outages accumulate the recovery backoff. 0 = adaptive recovery off (constant recovery).';
comment on column checks.flap_backoff_factor is 'Multiplies required recovery time per flap inside the window. 1 = off (constant recovery).';
comment on column checks.max_recovery_multiplier is 'Cap: required recovery never exceeds this multiple of the base recovery period (and a 30m hard ceiling).';
comment on column checks.flap_count is 'Outages accumulated inside the rolling flapping window. Written only on incident open/reopen.';
comment on column checks.last_outage_at is 'Wall-clock of the most recent outage onset; gates the flapping-window reset. NULL until the first outage.';

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
--                             lane-filtered SELECTs (benchmarked against a
--                             single composite index; equivalent within
--                             run-to-run noise — see make bench-checks).
--
-- Off-by-default-equivalent: penalty/credit = 0 and the caps default to the
-- pool size, so a fresh DB reproduces pure-FIFO behaviour.

alter table check_jobs add column cost_ewma_ms double precision not null default 0;
alter table check_jobs add column plan_weight smallint not null default 0;
alter table check_jobs add column effective_scheduled_at timestamptz;
alter table check_jobs add column delay_ewma_ms double precision not null default 0;
alter table check_jobs add column lane smallint not null default 0;

create index if not exists idx_check_jobs_claim_fast
    on check_jobs (effective_scheduled_at) where lane = 0;
create index if not exists idx_check_jobs_claim_slow
    on check_jobs (effective_scheduled_at) where lane = 1;

comment on column check_jobs.cost_ewma_ms is 'EWMA of execution duration in ms; timeouts pinned to the ceiling. Updated in the post-exec write. 0 until first run.';
comment on column check_jobs.plan_weight is 'Denormalized plan tier from org_entitlements. 0 = free; higher = more protected (reserved capacity + deadline credit). Refreshed on entitlement change + reconcile.';
comment on column check_jobs.effective_scheduled_at is 'scheduled_at + cost_ewma_ms×2 (clamped to 60s) − tier_credit. Claim ORDER BY key only; the gate is on scheduled_at. Backfilled to scheduled_at.';
comment on column check_jobs.delay_ewma_ms is 'EWMA of (probe start − scheduled_at) in ms, floored at 0. Pure telemetry (cost-distribution endpoint, lane-split go/no-go); never steers claim ordering. 0 until first run.';
comment on column check_jobs.lane is 'Scheduling lane: 0 = fast, 1 = slow. Classified from cost_ewma_ms with hysteresis (promote >= lane_slow_threshold_ms, demote < lane_fast_threshold_ms) in the post-exec release. Slow jobs are capped at pool_size − fast_lane_reserved in-flight per worker; fast jobs may use any slot.';
