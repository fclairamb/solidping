-- v0.28.0 — the ONE consolidated Postgres migration for the (still unreleased)
-- v0.28.0 release. 020_v0_26_0 shipped earlier, so everything this cycle
-- produces lands here, in a single file per dialect, per the repo convention
-- documented in wiki/conventions/database.md.
--
-- The SQLite twin (021_v0_28_0) carries one EXTRA section, label-key-check:
-- it brings SQLite onto a labels.key rule Postgres has enforced since its 001
-- baseline, so there is nothing to do here for it. It also carries a
-- parameter-key-check section, which is the MIRROR of the one below: this file
-- widens a CHECK only Postgres has, that one adds the widened rule to SQLite,
-- which has never had any CHECK on parameters.key at all.
--
--   SECTION: check-name-backfill        checks.name = slug where the name is blank
--   SECTION: parameter-key-hyphens      parameters.key CHECK gains the hyphen
--   SECTION: period-below-floor-backfill  raise a check's period to its type's
--                                          own default when it is still at the
--                                          flat 1m fingerprint AND below the
--                                          type's own MinPeriod
--
-- ⚠️ A DEV DATABASE THAT ALREADY RAN AN EARLIER DRAFT OF THIS FILE MUST BE
-- RESET, NEVER REPAIRED. bun keys an applied migration on its numeric prefix
-- alone, so appending a section to 021 does not re-run it: set
-- `SP_DB_RESET=true` (test/demo run mode) or drop and recreate the database.
-- **Do not run `solidping migrate repair`** — it rewrites the recorded
-- checksum without applying anything, so the database would report 021 as
-- applied while the check-name backfill below never ran and its nameless
-- checks stayed nameless. A database that looks correct and is not. See
-- wiki/conventions/database.md, "The unreleased series".

-- ==========================================================================
-- SECTION: check-name-backfill  (spec 2026-09-11-02)
--
-- Give every nameless check a name, so the org's own export is a document the
-- server can consume.
--
-- `name` was validated as "present" rather than "non-blank", so the API
-- accepted `""` (and a create that never resolved one left it NULL). Neither
-- shape is merely cosmetic: the v2 exporter omits an empty name, and both
-- ValidateDocument and the import path require `name` — so the instance
-- produced a config-as-code file it would itself reject with
-- `missing required key 'name'`. One tracked org hit exactly that and had to
-- drop the offending check out of its manifest.
--
-- The slug is the right fill: it is non-empty by construction, unique in the
-- org, and already the human-facing identifier of the check everywhere a name
-- is absent (see checkDisplayName, which has always fallen back to it). So
-- this changes nothing anyone sees — it writes down what the UI was already
-- displaying.
--
-- NULL is backfilled alongside `''` because it fails in exactly the same way:
-- both export as an absent `name`. Going forward the write paths refuse a
-- blank name on create and on update, so this runs once and never has work
-- again.
-- ==========================================================================

update checks
   set name = slug
 where slug is not null
   and slug <> ''
   and (name is null or btrim(name) = '');

--bun:split

-- ==========================================================================
-- SECTION: parameter-key-hyphens  (spec 2026-09-11-03, batch E2E gate)
--
-- Let a parameter key contain a hyphen.
--
-- The 001 baseline has enforced `key ~ '^[a-z0-9_\.]+$'` since v0.1.0 —
-- lowercase, digits, underscore, dot. No hyphen. That was fine while the only
-- writer was the platform itself (every internal key is dotted snake_case:
-- `encryption.dek`, `auth.session.max_duration`, `default_regions`).
--
-- Spec 2026-09-11-03 opened the table to organizations: an org admin creates
-- the values their check configs reference as `${param:KEY}`, and the key shape
-- the spec specifies — `^[a-z][a-z0-9_.-]{0,63}$` — allows a hyphen. Every
-- documented example uses one. `sso-authtest-password` is THE example, in the
-- spec, in wiki/features/config-as-code.md, on the docs site and in the
-- dashboard dialog's placeholder.
--
-- So the API accepted the key, the row hit this CHECK, and the operator got a
-- 500 with a raw SQLSTATE 23514 — on their first attempt, following the
-- documentation. Only on Postgres: SQLite's `parameters` table carries no CHECK
-- at all, which is why the entire feature's test suite passed while being
-- non-functional on the engine production runs. (That divergence is closed in
-- the SQLite twin's parameter-key-check section.)
--
-- Widening, not narrowing: hyphens were a deliberate spec decision, so the
-- constraint moves rather than the key rule.
--
-- This cannot reject a row the table already holds — the new character class is
-- the old one plus `-`, so it is strictly more permissive, and `alter table add
-- constraint` validates existing rows and would fail loudly if that were
-- untrue. It still refuses everything the constraint exists to refuse:
-- uppercase, whitespace, slashes, quotes, `%`, `:` and an empty key. Those
-- matter for system parameters in this same table too, which is why the rule is
-- widened by exactly one character and not relaxed.
-- ==========================================================================

alter table parameters drop constraint if exists parameters_key_check;

--bun:split

alter table parameters
  add constraint parameters_key_check check (key ~ '^[a-z0-9_.\-]+$');

--bun:split

-- ==========================================================================
-- SECTION: period-below-floor-backfill  (spec 2026-09-11-07)
--
-- Raise a check's period to its type's own default when it is BOTH below that
-- type's MinPeriod AND exactly equal to models.NewCheck's flat one-minute
-- constant — the fingerprint of "the server picked this because the create
-- request supplied no period", not a value any human ever typed. A row at some
-- other below-floor value (say 30m on a dnsbl check) was typed by a human
-- through some earlier path and is left alone: validatePeriodForType's own
-- comment states the standing decision that existing rows are grandfathered,
-- and this backfill is narrower than that decision, not an exception to it —
-- these particular rows were never a value anyone chose.
--
-- Without this, an org whose ssl/domain/dnsbl check was created with no
-- period keeps a period below its own type's floor forever, and its own
-- GET /checks/export document keeps failing its own POST /checks/import
-- (period for ssl checks must be at least 1h) — the exact bug CreateCheck's
-- new defaultPeriodForType resolver (server/internal/handlers/checks/
-- validate.go) fixes going forward. This is the one-time catch-up for rows
-- created before the fix shipped.
--
-- PERIOD_BACKFILL_TYPES: browser, dnsbl, domain, js, ssl
--
-- The list above is EVERY checkerdef type that declares a MinPeriod > 0 today
-- — TestPeriodBackfillTypeListMatchesCheckerdef (checkerdef package) pins it
-- against checkerdef.ListCheckTypeMetas so a future type with a floor cannot
-- be silently missed here. `js` and `browser` are included for completeness
-- even though their own floor (30s, 1m) sits at or below the flat one-minute
-- fingerprint, so their branch of the WHERE clause below never matches any
-- row — that is what makes the parity test meaningful rather than trivially
-- true (a type list that only ever named the 3 types that DO need backfill
-- today would still pass a test that only checked "no floor type is missing
-- from a hardcoded 3-item list").
-- ==========================================================================

update checks
   set period = case type
                  when 'ssl'     then interval '6 hours'
                  when 'domain'  then interval '24 hours'
                  when 'dnsbl'   then interval '1 hour'
                  when 'js'      then interval '1 minute'
                  when 'browser' then interval '5 minutes'
                end
 where type in ('ssl', 'domain', 'dnsbl', 'js', 'browser')
   and period = interval '1 minute'
   and period < case type
                  when 'ssl'     then interval '1 hour'
                  when 'domain'  then interval '6 hours'
                  when 'dnsbl'   then interval '15 minutes'
                  when 'js'      then interval '30 seconds'
                  when 'browser' then interval '1 minute'
                end;
