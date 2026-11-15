-- ==========================================================================
-- SECTION: worker-boot-id
-- Spec 2026-11-14-03.
-- ==========================================================================
-- A deported agent sends a random boot ID, generated once per process, on
-- enroll and on every reconnect. The server keeps the last one per worker row
-- and frees the worker's check-job leases only when it changes: a restarted
-- process cannot finish the runs its predecessor leased, while a network
-- reconnect from the same process still can.
alter table workers add column if not exists boot_id text;

comment on column workers.boot_id is 'Random ID of the agent process that last connected, sent on enroll and reconnect. A change means a new process: its leases are freed. NULL: never reported (in-process worker, or an agent too old to send one).';
