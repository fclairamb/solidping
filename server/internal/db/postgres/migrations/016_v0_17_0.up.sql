-- v0.17.0 — replace the `results.abandoned` boolean with the dedicated
-- ResultStatusAbandoned (9) status value (spec 2026-08-18-10).
--
-- 015 encoded a reaped attempt as "status = error, plus an invisible asterisk"
-- (`abandoned = true`). The two axes never actually vary independently — the
-- reaper is the flag's only writer, the state is terminal, and there is no
-- "abandoned but some other status" combination — so the flag collapses into
-- one more case of the status enum it was shadowing. Rendering a reaped
-- attempt as `error` was also less honest than a status of its own:
-- everywhere else `error` means the attempt ran and failed, and a reader then
-- had to know about a second column to learn that this particular error is
-- quietly un-counted from availability.
--
-- This is a NEW migration rather than an edit of 015 on purpose: 015 has
-- already been applied on dev databases, bun keys an applied migration on its
-- numeric prefix alone (so an edit would never re-run), and
-- internal/db/migrationguard checksums applied `.up.sql` files and refuses to
-- boot on a mismatch. 016 is likewise the next FREE number — the gap at 014 is
-- a withdrawn migration whose number still lives in some `bun_migrations`, and
-- sliding into it would be silently skipped. See wiki/conventions/database.md.
--
-- The three steps below MUST run in this order:
--   1. widen the status CHECK domain, or step 2 is rejected by it;
--   2. convert the existing reaped rows, while `abandoned` still exists to
--      identify them;
--   3. drop the column.

-- 1. Widen `status in (0..8)` to `(0..9)`. The constraint is an unnamed inline
--    column check from 001, so Postgres named it itself; rather than betting on
--    `results_status_check`, drop whichever check constraint on `results`
--    actually mentions `status` (there is exactly one — the only other check on
--    the table constrains period_type).
do $$
declare
  con record;
begin
  for con in
    select c.conname
    from pg_constraint c
    join pg_class t on t.oid = c.conrelid
    join pg_namespace n on n.oid = t.relnamespace
    where t.relname = 'results'
      and n.nspname = current_schema()
      and c.contype = 'c'
      and pg_get_constraintdef(c.oid) ilike '%status%'
  loop
    execute format('alter table results drop constraint %I', con.conname);
  end loop;
end
$$;

--bun:split

-- Validating (not NOT VALID): the new domain is a strict superset of the one
-- just dropped, so the scan can only confirm what is already true, but it
-- keeps the constraint indistinguishable from a freshly-created schema's — the
-- release-time golden-schema diff compares them.
alter table results
  add constraint results_status_check check (status in (0, 1, 2, 3, 4, 5, 6, 7, 8, 9));

--bun:split

-- 2. Convert the rows 015's reaper produced. `and abandoned` is what keeps the
--    backfill from sweeping genuine `error` rows — a real failure the monitored
--    service produced must keep counting against availability. Re-running is a
--    no-op: no row is left with `status = 6 and abandoned` afterwards.
update results
set status = 9
where period_type = 'raw'
  and status = 6
  and abandoned;

--bun:split

-- 3. The column comment added by 015 is dropped along with the column.
alter table results drop column if exists abandoned;

--bun:split

-- 001's comment on this column has been wrong since the enum was renumbered;
-- refresh it now that 9 joins the domain.
comment on column results.status is
  '1=created, 2=running, 3=up, 4=down, 5=timeout, 6=error, 7=degraded (aggregated only), 8=warning, 9=abandoned (reaper-minted, excluded from availability).';
