---
model: opus
effort: high
---

# A rewritten migration 013 is silently skipped, breaking check execution and the raw-data rendering on the check detail page

## Problem

The check detail page (`/dash0/orgs/default/checks/2128c982-0753-402e-a64b-3532509d390f`,
an FTP check) is visibly broken on the local dev instance:

- The Response Times chart shows **"No data available"** on every range — the raw
  tier has no rows at all.
- The header shows **"Pending first run…"** while simultaneously claiming
  "Currently up for 2d 10h" and "Last checked 20d ago".
- `GET /api/v1/orgs/default/regions` returns **404** on page load.
- The only remaining `periodType=raw` row for the check is a stuck
  `status: "created"` row from **2026-07-28** (which is what "Last checked 20d ago"
  is derived from); hour rollups stop at **2026-08-16T05:59Z** and nothing has been
  recorded since.

The user first suspected the recent batch specs (2026-08-17-04/05/06), but the
proximate cause is earlier and different. The backend dev log shows, on every page
load:

```
level=WARN msg="SQL query failed" operation=SELECT
  error="SQL logic error: no such column: worker.capabilities (1)"
```

Root cause, verified against both local SQLite dev DBs (`./solidping.db` and
`./server/solidping.db`):

- `bun_migrations` records migration **013 as applied on 2026-08-13**.
- But the current [013_v0_16_0.up.sql](server/internal/db/sqlite/migrations/013_v0_16_0.up.sql)
  was only *created on 2026-08-16* by commit `a5ee7c439` (PR #226, the previous
  batch's squash-merge — prometheus checks, RDAP, three-state region capabilities).
  During that batch, the migration content evolved under the same number *after*
  the running devloop had already applied an earlier version of it.
- Result: the final content — `alter table workers add column capabilities text`
  ([013_v0_16_0.up.sql:27](server/internal/db/sqlite/migrations/013_v0_16_0.up.sql))
  plus the two `workers_capabilities_shape_*` triggers — **silently never ran**.
  Verified: `pragma_table_info('workers')` has no `capabilities` column and no
  matching triggers exist, while the migration is marked applied.

Downstream, every query referencing `worker.capabilities` fails on such a DB:
worker/region resolution breaks (`/regions` → 404), check scheduling stops
dispatching (no results written since the devloop picked up the new code around
2026-08-16 06:00Z), so the raw tier the chart queries
([response-time-chart.tsx:176-190](web/dash0/src/components/checks/response-time-chart.tsx))
is empty and the page degrades as described.

This is the same failure mode already hit once before (see
`project_migration_consolidation_stale_db`): a migration file renumbered or
rewritten after being applied desyncs `bun_migrations`, and nothing detects it —
the app boots and limps along with per-query WARNs instead of failing loudly.

**Do not hand-repair the local dev DBs before implementing** — they are the live
reproduction and should be healed by the fix itself.

## Proposal

Two layers — repair the incident, then make the failure mode impossible to miss:

1. **Self-healing follow-up migration (014)**, for both engines, that idempotently
   applies the missed DDL:
   - Postgres: `ALTER TABLE workers ADD COLUMN IF NOT EXISTS capabilities ...`
     plus guarded trigger/constraint creation, matching what 013 should have left
     behind (Postgres dev DBs migrated mid-batch can be desynced the same way).
   - SQLite has no `ADD COLUMN IF NOT EXISTS`: use a Go migration (bun supports
     them) that checks `pragma_table_info('workers')` / `sqlite_master` before
     applying the column and the two shape triggers.
   - After 014, a desynced DB and a correctly-migrated DB must be schema-identical.

2. **Migration integrity guard at startup**: record a content checksum per applied
   migration (side table or extra column next to `bun_migrations`) and compare on
   boot. On mismatch, fail startup with a clear, actionable error naming the
   migration and the repair options (reset the dev DB, or run the documented
   reconcile) — never silently skip. Backfill checksums for already-applied
   migrations on first boot so existing healthy DBs don't trip the guard.

3. **Verification**: with the currently-desynced `./solidping.db`, boot the fixed
   server and confirm (a) 014 adds the column and triggers, (b) `/api/v1/orgs/default/regions`
   returns 200, (c) the check resumes executing and new `periodType=raw` rows
   appear, and (d) the check detail page renders the Response Times chart with
   data again. Add a regression test that applies a migration, mutates its
   recorded content, and asserts boot fails with the checksum error.

## Open questions

- The stuck `status: "created"` raw row from 2026-07-28 drives a nonsensical
  "Last checked 20d ago" — is there a reaper that should finalize or discard
  abandoned `created` results? Possibly a separate small spec.
- `pagination.total: 0` was returned alongside a non-empty `data` array on
  `/api/v1/orgs/default/results?periodType=raw` — minor inconsistency, worth a
  look while in the area.
- During browsing the content pane once went fully blank after a scroll, with
  "An unknown error occurred when fetching the script" (Vite dev chunk fetch) and
  a React "Cannot update a component (`Transitioner`) while rendering `OrgLayout`"
  warning in the console. Likely dev-server noise unrelated to the root cause,
  but re-check once data flows again; file separately if it persists.

## Resolved open questions

> The stuck `status: "created"` raw row from 2026-07-28 drives a nonsensical
> "Last checked 20d ago" — is there a reaper that should finalize or discard
> abandoned `created` results? Possibly a separate small spec.

**Decision: out of scope — file it as its own spec.** Do not add a reaper here.
This spec is already large (a 014 self-healing migration for two engines plus a
startup checksum guard); abandoned-result reaping is unrelated machinery and
belongs in its own review. Write the follow-up spec into `specs/todos/` as part
of this work, and accept that the nonsensical "Last checked" reading persists
until that spec lands.

> `pagination.total: 0` was returned alongside a non-empty `data` array on
> `/api/v1/orgs/default/results?periodType=raw` — minor inconsistency, worth a
> look while in the area.

**Decision: fix in-scope if it is trivial; otherwise defer to its own spec.**
Investigate the mismatch between the `COUNT` and the row query on the results
endpoint. If the cause is a small filter/parameter divergence, fix it here and
add a regression test pinning `pagination.total` against the length of `data`
for a known fixture. If it turns out to be structural (a different pagination
model, a rollup-tier interaction), do **not** widen this spec — write it up
separately instead. A wrong `total` silently breaks pagination for API
consumers, which is why it is worth the look.

> During browsing the content pane once went fully blank after a scroll, with
> "An unknown error occurred when fetching the script" (Vite dev chunk fetch)
> and a React "Cannot update a component (`Transitioner`) while rendering
> `OrgLayout`" warning in the console.

**Decision: treat as dev-server noise and out of scope.** The Vite chunk-fetch
failure is a normal HMR artifact after a dev-server restart, which this
incident caused repeatedly. Re-check the page once the migration fix restores
data flow; file a separate spec **only if it reproduces on a healthy server**.
Do not chase the `Transitioner`-during-render warning as part of this spec — it
is a dash0 routing concern with no connection to the migration root cause.

## Implementation Plan

### 1. Migration 014 — self-healing re-application of 013's DDL

Next release after `v0.16.2` is `v0.17.0` (the batch carries `feat:` commits), so the
migration is named `014_v0_17_0`.

**Postgres** — `server/internal/db/postgres/migrations/014_v0_17_0.up.sql` /
`.down.sql`. Every statement 013 runs is already idempotent
(`add column if not exists`, `drop constraint if exists` then `add constraint`), so
014 re-issues the same block verbatim. `.down.sql` is a deliberate no-op: 013's down
already removes the column and the constraint, and 014 must never undo a healthy
schema.

**SQLite** — SQLite has no `ADD COLUMN IF NOT EXISTS`, so 014 is a **Go migration**
registered through `migrate.Migrations.Register`, whose name/comment bun derives from
the *caller's file name*. New package
`server/internal/db/sqlite/gomigrations/014_v0_17_0.go` exporting
`Register(*migrate.Migrations) error`, wired into `sqlite.Service.Initialize` right
after `Discover`. The up function:

- reads `pragma_table_info('workers')`; if `capabilities` is absent it issues the
  **byte-identical** `alter table workers add column capabilities text check (...)`
  statement text from 013 — SQLite appends the literal column-definition text to the
  stored `CREATE TABLE`, so identical text is what makes a healed DB and a freshly
  migrated DB schema-identical;
- reads `sqlite_master` for `workers_capabilities_shape_insert` /
  `workers_capabilities_shape_update` and creates each missing one from the same
  literal body as 013.

Acceptance: a desynced DB healed by 014 and a fresh DB must have identical
`sqlite_master.sql` for `workers` and for both triggers. Pinned by a test.

### 2. Migration integrity guard

New package `server/internal/db/migrationguard`:

- `Checksums(fsys, dir)` — sha256 over each `NNN_*.up.sql`, keyed by the numeric
  prefix bun keys on. Go migrations contribute an explicit constant checksum.
- `Guard.Reconcile(ctx)` — creates the side table `migration_checksums`
  (`name` PK, `checksum`, `recorded_at`), reads `bun_migrations`, and for every
  applied migration either **backfills** the current checksum (first boot on an
  existing DB) or **compares** it, returning a `MismatchError` naming the migration,
  both checksums and the repair options.

Wired into both `Initialize` implementations: `Reconcile` before `Migrate` (verify +
backfill what is already applied) and again after `Migrate` (record what was just
applied). Down-migration files are deliberately out of the checksum: editing a
`.down.sql` never desyncs an applied schema.

### 3. Verification & tests

- SQLite: build a DB that reproduces the incident exactly (run migrations, then drop
  the column and triggers and leave `bun_migrations` claiming 013 applied), re-run
  `Initialize`, assert the column, both triggers, the constraint verdicts
  (`dbcaptest.SharedCases`) and the raw `sqlite_master.sql` all match a fresh DB.
- Postgres: same shape via testcontainers.
- Guard regression: boot, tamper with the recorded checksum of an applied migration,
  boot again, assert `Initialize` fails with the mismatch error naming the migration.
  Plus a positive control: an untampered second boot succeeds.
- Live: boot a side-car server on an alt port against a copy of the desynced
  `./solidping.db`, confirm 014 heals it and `/api/v1/orgs/default/regions` answers
  200; then let the `:4000` devloop heal `server/solidping.db` and confirm new
  `periodType=raw` rows and the check-detail response-time data.

### 4. Resolved-open-question follow-ups

- Abandoned `status: "created"` results reaper → written up as its own spec in
  `specs/todos/`, not implemented here.
- `pagination.total: 0` on `/results?periodType=raw` → investigate the COUNT-vs-rows
  divergence; fix here with a regression test only if trivial, otherwise its own spec.

## Addendum (2026-08-18)

Migration 014 (the self-healing re-application of 013, both dialects) was **removed
before release**: 013 never shipped in any tagged version, so the only databases
carrying the bad draft were development ones, and those were repaired by hand
instead of shipping a permanent no-op migration to every future install. The
startup migration-integrity guard (`internal/db/migrationguard`) — the part of this
spec that prevents recurrence — stays.
