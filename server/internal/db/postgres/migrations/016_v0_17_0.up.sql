-- v0.17.0 — replace the `results.abandoned` boolean with the dedicated
-- ResultStatusAbandoned (9) status value (spec 2026-08-18-10).
--
-- 015 encoded a reaped attempt as "status = error, plus an invisible asterisk"
-- (`abandoned = true`). The two axes never actually vary independently — the
-- reaper is the only writer, the state is terminal, and there is no "abandoned
-- but some other status" combination — so the flag collapses into one more
-- case of the status enum it was shadowing. Rendering a reaped attempt as
-- `error` was also less honest than a status of its own: everywhere else
-- `error` means the attempt ran and failed, and a reader then had to know
-- about a second column to learn that this particular error is quietly
-- un-counted from availability.
--
-- This is a NEW migration rather than an edit of 015 on purpose: 015 has
-- already been applied on dev databases, bun keys an applied migration on its
-- numeric prefix alone (so an edit would never re-run), and
-- internal/db/migrationguard checksums applied `.up.sql` files and refuses to
-- boot on a mismatch. 016 is likewise the next FREE number — the gap at 014 is
-- a withdrawn migration whose number still lives in some `bun_migrations`, and
-- sliding into it would be silently skipped. See wiki/conventions/database.md.
--
-- Order matters: convert first, drop second, or the conversion loses the only
-- evidence of which rows were reaped. The `and abandoned` predicate is what
-- keeps the backfill from sweeping genuine `error` rows — a real failure the
-- monitored service produced must keep counting against availability.

update results
set status = 9
where period_type = 'raw'
  and status = 6
  and abandoned;

--bun:split

-- The column comment added by 015 is dropped along with the column.
alter table results drop column if exists abandoned;
