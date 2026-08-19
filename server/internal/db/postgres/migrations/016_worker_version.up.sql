-- Workers self-report their own build version (spec 2026-08-19-07), so fleet
-- version drift becomes visible instead of requiring a shell into every
-- deployment target. Mirrors the nullable/no-backfill discipline of the
-- `capabilities` column recipe in 013_v0_16_0.up.sql, adapted for a SCALAR
-- rather than a set:
--
--   NULL       unknown — nothing was ever reported (an agent predating this
--              feature, or a worker that has not sent a claim frame yet)
--   'x.y.z'    the version this worker last reported
--
-- Unlike `capabilities`, there is no meaningful "reported but empty" third
-- state for a single version string — a real build version is never the
-- empty string — so this is a two-state column, not three. NULL is still the
-- ONLY unknown, and it must never be rendered as "drifted": an old agent that
-- predates version reporting must not look broken. This is detection only —
-- nothing here blocks, throttles or disconnects an agent on a stale version.
--
-- Numbering: this is a scratch migration for the still-unreleased v0.17.0
-- cycle (see wiki/conventions/database.md's development workflow). 013 is
-- the last RELEASED migration; 014_v0_17_0 is that cycle's consolidated file
-- so far; 015 removed the Opsgenie integration type. 016 is simply the next
-- free scratch number — it folds into the final 014_v0_17_0 at release
-- consolidation, same as every other scratch migration this cycle.

alter table workers add column if not exists version text;

--bun:split

comment on column workers.version is
  'Self-reported build version (internal/version.Get().Version). NULL = never reported (unknown) — must never be rendered as "drifted". Never an empty string — see the CHECK.';

--bun:split

alter table workers drop constraint if exists workers_version_not_empty;

--bun:split

-- A plain scalar CHECK, unlike capabilities' element-shape regex: there is
-- exactly one value to validate here, not a set of them, so there is nothing
-- for a charset/duplicate rule to apply to. This only buys back "empty string
-- is not a version" — the one way a scalar column could silently relearn the
-- three-state confusion the capabilities migration exists to avoid.
alter table workers add constraint workers_version_not_empty check (
  version is null or version <> ''
);
