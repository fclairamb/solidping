-- ==========================================================================
-- SECTION: sealed-secrets-peer-rewrap (spec 2026-11-10-03)
-- ==========================================================================
-- A task only the named worker may claim: a sealed-secrets re-wrap goes to an
-- agent that holds a key the blob was sealed to, never to any agent of the
-- region.
alter table worker_tasks add column if not exists target_worker_uid text;

--bun:split

comment on column worker_tasks.target_worker_uid is 'Worker (agent uid) that alone may claim the task, e.g. the recipient agent of a sealed-secrets re-wrap. NULL when any worker of the region may claim it.';
