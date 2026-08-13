-- Down migration for the consolidated v0.14.0 release migration.
-- Teardown/parity only — never run in production.
--
-- PART 1 (user_integration_identities) reverses cleanly: the table is new, so
-- dropping it loses only mappings that can be rebuilt by re-running the Slack
-- auto-match.
--
-- PART 2 (private regions become org-relative) is deliberately NOT reversed.
-- The up migration is LOSSY in both directions: the org slug is gone from the
-- string (it lived nowhere else), and the rows de-duplicated by the collapse are
-- gone. Re-deriving `@<org>/<slug>` from the row's organization_uid would
-- produce the org's CURRENT slug, which is exactly the stale-copy bug the
-- migration exists to remove — a "rollback" would silently re-strand every agent
-- enrolled before a rename.
--
-- Rolling back the code without rolling back that data is safe: the old code
-- reads `agents.region` and `checks.regions` verbatim and matches them on exact
-- equality, so an install left on `@<slug>` on both sides keeps working.

drop index if exists user_integration_identities_organization_idx;
drop index if exists user_integration_identities_integration_external_idx;
drop index if exists user_integration_identities_integration_user_idx;
drop table if exists user_integration_identities;
