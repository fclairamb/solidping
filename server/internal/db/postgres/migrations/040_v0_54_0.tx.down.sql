-- v0.54.0 — postgres rollback (parity only; downs are never run in production).

-- SECTION: agent-auto-upgrade
drop table if exists agent_upgrade_candidates;
drop table if exists agent_upgrade_slots;
