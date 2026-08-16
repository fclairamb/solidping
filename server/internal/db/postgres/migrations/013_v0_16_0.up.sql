-- v0.16.0 — per-worker egress capability (spec 2026-08-15-11).
--
-- Workers self-report which address families they can originate traffic over,
-- so the region list can tell a user "this region does IPv6" BEFORE they create
-- a check that fails on its first run.
--
-- Both columns are NULLABLE and there is deliberately NO BACKFILL: null means
-- "unknown", which is the only honest value for a row written by an agent that
-- predates the feature. Defaulting them to false would claim every existing
-- worker has no IPv6 — the exact false statement this change exists to remove —
-- and it is what makes the rollout order (server first or agent first)
-- irrelevant.

alter table workers add column if not exists egress_ipv4 boolean;

--bun:split

alter table workers add column if not exists egress_ipv6 boolean;
