-- v0.56.0 — sqlite migration.
--
--   SECTION: agent-outdated-notice   workers.version_drifted_since, workers.outdated_notified_version
--
-- ==========================================================================
-- SECTION: agent-outdated-notice
-- ==========================================================================
-- Outdated private-location agent notice (spec 2026-11-26-03).
-- version_drifted_since is when the agent first reported a version other than
-- the server's (NULL while it matches); outdated_notified_version is the server
-- version the org was last told about, so each agent is mentioned once per
-- server version.
alter table workers add column version_drifted_since text;

--bun:split

alter table workers add column outdated_notified_version text;
