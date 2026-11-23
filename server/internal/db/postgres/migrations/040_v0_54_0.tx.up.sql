-- v0.54.0 — postgres migration.
--
--   SECTION: agent-auto-upgrade   agent_upgrade_slots, agent_upgrade_candidates
--   SECTION: agent-version-pin    workers.version_pinned
--
-- ==========================================================================
-- SECTION: agent-auto-upgrade
-- ==========================================================================
-- Rolling agent upgrade (spec 2026-11-22-04). A connected agent whose build
-- version differs from the server's is asked to restart on the server's
-- version, at most one agent per region at a time across every server
-- replica. The coordination is shared, so it lives here.
--
-- agent_upgrade_slots: one row per region (`region_key` is the owning org UID,
-- or `system`, then `|` and the region slug: an org agent's region is
-- org-relative). The holder is the agent currently asked to upgrade; the slot
-- is free once `released` or past `deadline`. `generation` makes the takeover
-- a compare-and-swap.
create table if not exists agent_upgrade_slots (
  region_key     text        primary key,
  agent_uid      text        not null,
  target_version text        not null,
  deadline       timestamptz not null,
  released       boolean     not null default false,
  generation     bigint      not null default 0,
  updated_at     timestamptz not null default now()
);

--bun:split

-- agent_upgrade_candidates: the drifted agents currently connected (seen_at is
-- refreshed by their connection), and how many times each was asked to
-- upgrade to target_version. The next agent asked is the one asked the fewest
-- times, so a timed-out agent is not retried before the others.
create table if not exists agent_upgrade_candidates (
  agent_uid      text        primary key,
  region_key     text        not null,
  target_version text        not null,
  attempts       integer     not null default 0,
  seen_at        timestamptz not null default now(),
  created_at     timestamptz not null default now()
);

--bun:split

create index if not exists idx_agent_upgrade_candidates_region on agent_upgrade_candidates (region_key);

--bun:split

-- ==========================================================================
-- SECTION: agent-version-pin
-- ==========================================================================
-- An agent started with SP_AGENT_IGNORE_VERSION_DRIFT=true announces
-- autoUpgrade: false (spec 2026-11-22-04). The server skips it in the rollout
-- and the dashboard labels its drift "Pinned". Refreshed on each connection's
-- first report; false (the default) for every other worker.
alter table workers add column version_pinned boolean     not null default false;
