-- solidping v0.7.0 — org-level default escalation policy (spec 2026-07-19-01).
-- A nullable pointer from an organization to the escalation policy that applies
-- to any of its checks that resolve to no policy of their own (check → group →
-- ORG DEFAULT → none). Opt-in: unset (NULL) reproduces today's behavior exactly.
-- `on delete set null` mirrors checks.escalation_policy_uid / check_groups: when
-- the referenced policy is deleted, the pointer clears rather than blocking the
-- delete or dangling.
alter table organizations
  add column default_escalation_policy_uid uuid
    references escalation_policies(uid) on delete set null;

comment on column organizations.default_escalation_policy_uid is
  'Org-wide fallback escalation policy for checks that resolve to no policy (check > group > org default > none). NULL = no org default (legacy behavior).';
