-- ==========================================================================
-- SECTION: sealed-secrets-peer-rewrap (spec 2026-11-10-03)
-- ==========================================================================
-- worker_tasks.target_worker_uid: the worker (agent uid) that alone may claim
-- the task, e.g. the recipient agent of a sealed-secrets re-wrap. NULL when any
-- worker of the region may claim it.
alter table worker_tasks add column target_worker_uid text;
