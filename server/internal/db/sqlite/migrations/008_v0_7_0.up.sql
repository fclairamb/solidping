-- solidping v0.7.0 — org-level default escalation policy (spec 2026-07-19-01).
-- SQLite mirror of the Postgres migration. A nullable pointer from an
-- organization to the escalation policy applied to any of its checks that
-- resolve to no policy of their own (check > group > ORG DEFAULT > none).
-- Opt-in: unset (NULL) reproduces today's behavior exactly. `on delete set
-- null` mirrors checks.escalation_policy_uid / check_groups. SQLite permits a
-- REFERENCES clause on ADD COLUMN because the added column defaults to NULL.
alter table organizations
  add column default_escalation_policy_uid text
    references escalation_policies(uid) on delete set null;
