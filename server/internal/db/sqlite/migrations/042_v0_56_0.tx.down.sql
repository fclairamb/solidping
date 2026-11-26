-- v0.56.0 — sqlite rollback (parity only; downs are never run in production).

-- SECTION: agent-outdated-notice
alter table workers drop column outdated_notified_version;
alter table workers drop column version_drifted_since;
