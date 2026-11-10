-- SECTION: sealed-secrets-peer-rewrap
alter table worker_tasks drop column if exists target_worker_uid;
