-- Down migration for the 013 self-heal. Deliberately a no-op.
--
-- 014 creates nothing of its own — it only finishes 013 — and 013's own down
-- migration drops `workers.capabilities` and its constraint. Dropping them here
-- too would leave a database rolled back to 013 in exactly the broken state 014
-- exists to repair.

select 1;
