# Table conventions
- All entities have a `uid` UUID (external/API)
- All entities except global ones have an `organization_uid` reference
- All tables should be in plural (`organizations`, `users`, `checks`)
- All link tables should have the plural-plural forms (`properties_assets` that links `properties` and `assets`)
- Soft deletes: `deleted_at` timestamp, never hard delete
- Audit trail: `created_at` and `updated_at` timestamps

# Column or `settings` JSON? (spec 2026-08-22-03)

Several tables carry a typed JSON `settings` column (`status_pages.settings`
decodes into `models.StatusPageSettings`). New per-entity knobs keep landing as
columns anyway, one migration section, one bun tag and one `Set(...)` branch per
dialect at a time, because the rule was never written down. It is:

> A per-entity knob read **only while rendering** belongs in `settings`.
> A field that is **filtered, joined, uniquely constrained, resolved by an
> external lookup, or is a credential** belongs in a column.

Worked example, `status_pages`:

| Field | Home | Why |
|---|---|---|
| `settings.availability.*`, `settings.branding.*` | JSON | read only while rendering the page |
| `visibility`, `enabled` | column | filtered in every list query |
| `custom_domain*` | column | globally unique, resolved by `Host` on every request |
| `password_hash` | column | a credential, read on the auth path, never serialized |
| `custom_css` | column | *grandfathered* — released, behaviourally identical either way, and moving it is a backfill over released rows for zero behaviour change |

Two consequences worth stating, because they are what makes the JSON side safe:

- **Never read-modify-write the whole column in Go** to change one section. That
  clobbers a concurrent write to a sibling section. Merge in SQL:
  Postgres `settings || jsonb_build_object('<section>', coalesce(settings->'<section>', '{}'::jsonb) || ?::jsonb)`
  (`||` is shallow, so the nesting is load-bearing), SQLite
  `json_patch(settings, ?)` (RFC 7386, already recursive).
- **The two dialects disagree on a null**: `json_patch` REMOVES the key, `||`
  STORES `null`. They agree on what it decodes to. Pin that with a parity test
  rather than trusting it.

A knob that later needs an index is a knob that stopped being render-only —
promote it to a column then, with a backfill, rather than pre-paying for it.

# Indexes conventions
Indexes should have the name `${table}_${columns}_idx`, the columns should not contain `_uid`.
So for example `incidents_organization`.

# Migrations

## One migration per release

Each release contributes **exactly one migration file** per engine, consolidating all schema changes since the previous release. The naming pattern is:

```
NNN_vX_Y_Z.up.sql   — forward migration (creates all schema for that release)
NNN_vX_Y_Z.down.sql — reverse migration (drops everything; parity only)
```

Example: `001_v0_1_0.up.sql` for v0.1.0.

Migration files live in:
- `server/internal/db/postgres/migrations/` (Postgres)
- `server/internal/db/sqlite/migrations/` (SQLite)

Both directories are always kept in sync and use the same `NNN` sequence number.

## Filename constraints

Bun's migration discovery regex (`[0-9a-z_\-]` only) does **not** allow dots. Use underscores for version separators: `001_v0_1_0`, not `001_v0.1.0`.

## Freeze rule

Migrations already shipped in a released version (`vX.Y.Z` git tag) are **frozen and never rewritten**. Only add new files for new releases.

## Gaps in the `NNN` sequence — never renumber a RELEASED one, always consolidate an UNRELEASED series

Two rules that look contradictory live here. They are not: they are about
different halves of the sequence, split at the last released migration.

### Released or withdrawn numbers: leave the hole

Once a number has escaped into any database that is not yours — a released
tag, or a withdrawn draft some colleague's dev database still records — the
hole it leaves is permanent. **Leave it.** Bun discovers migrations by numeric
prefix and applies whichever are unapplied, in order; a gap costs nothing.

Renumbering a later migration down into such a hole is actively dangerous,
because the hole is only empty *in the source tree*. Databases that ran the
withdrawn migration still carry its number in `bun_migrations`. Slide the next
migration into that number and bun sees it as already applied and **silently
skips it** — the exact failure mode the integrity guard below exists to catch,
reintroduced by hand. On those databases the guard then refuses to boot on the
checksum mismatch, which is correct but means a hand repair before anything
runs again.

### The unreleased series: consolidate it, renumber included

Everything numbered above the last released migration is still a draft. The
"one migration per release" rule at the top of this section is the goal state,
and the [development workflow](#development-workflow) below is how you get
there: at release time the cycle's scratch migrations collapse into a single
`NNN_vX_Y_Z` file per engine holding the **net final DDL**. That consolidation
is a renumbering, and it is correct — the numbers being reused never shipped.

Consolidate as soon as the churn is visible, not only on release day: a series
where one migration adds a column and the next drops it makes every future
install create the column only to destroy it, and on SQLite that is a full
rebuild of the largest table in the system for no net gain. (v0.17.0 did
exactly this across `015` + `016`; both were replaced by a single `014` — `013`
being the last released migration, `014` is simply the next number.)

The price is paid entirely by development databases, and it is paid by
**RESET**, never by repair:

- Reset the database — `SP_DB_RESET=true` in test/demo run mode, or delete the
  SQLite file.
- **Do not run `solidping migrate repair`.** Repair rewrites the recorded
  checksum without applying any schema. A database that ran the old series
  would come out claiming the consolidated migration is applied while actually
  carrying whatever the old files left behind — a database that looks correct
  and is not.

Say so explicitly in the consolidated migration's header and in the release
notes, so nobody reaches for repair out of habit.

## Integrity guard: an applied migration is frozen too

The freeze rule above is about *released* migrations. The trap is the pending
one: bun keys an applied migration on its **numeric prefix alone**, so editing
`013_v0_16_0.up.sql` after a dev loop has already run an earlier draft of it
does **not** re-run it. The new statements never execute, `bun_migrations` keeps
claiming 013 is applied, and the database silently lacks whatever the rewrite
added. That has bitten this repository twice — most recently as a missing
`workers.capabilities` column that 404'd region resolution and stopped check
scheduling for two days (spec 2026-08-18-02).

`internal/db/migrationguard` makes that loud. On every boot, before migrating,
it compares a SHA-256 of each applied migration's `.up.sql` against the
checksum recorded in the `migration_checksums` side table:

- **No record** (a database that predates the guard) → the checksum is
  backfilled and the boot proceeds. Existing healthy databases never trip it.
- **Record matches** → normal boot.
- **Record differs** → what happens depends on the guard mode (below).

Only the `.up.sql` half is hashed: a `.down.sql` never runs during a forward
boot, so editing one cannot desync an applied schema. A Go migration would
have no file to hash; `migrationguard.New` accepts declared checksums for
that case (no current occupant).

### Guard mode: `strict` (default) or `warn`

`db.migration_guard_mode` / `SP_DB_MIGRATION_GUARD_MODE` (**multi-word koanf
key — read by the manual `applyMigrationGuardModeEnv` reader, not the
automatic env loader**, same quirk as `db.slow_query_threshold`) picks the
behavior on a checksum mismatch:

- **`strict`** (the default, and the only mode production should run) —
  startup **fails** with an error naming the migration, both checksums, and
  the repair options. Nothing is written to `migration_checksums` while any
  mismatch exists, not even for an unrelated migration awaiting its first
  checksum — a database that is already diverged must not have its record
  quietly updated.
- **`warn`** — the mismatch is logged (`slog.WarnContext`, once per boot) and
  the boot continues. `make dev` / `make dev-test` / `make dev-saas` always run
  this way (set in the root `Makefile`), because editing a comment in an
  already-applied migration during local development is common and must not
  brick the dev database. Warn mode still backfills any migration with no
  recorded checksum at all, even while another migration mismatches — only the
  mismatched row's own checksum is protected from being overwritten, in both
  modes, which is what makes the warning recur every boot until someone
  repairs deliberately.

### Repairing a database the guard rejects

1. **Cosmetic edit** (comment/whitespace only in an already-applied
   migration) — run `solidping migrate repair`. It re-records checksums for
   every applied migration this binary ships (inserting a missing row or
   updating a drifted one to the current file's checksum) **without running
   any migration** — it only ever touches `migration_checksums`.
2. **Development** — reset the database (`SP_DB_RESET=true` in test/demo run
   mode, or delete the SQLite file), or run with
   `SP_DB_MIGRATION_GUARD_MODE=warn` to boot anyway.
3. **Otherwise** — apply the missing statements by hand, then run
   `solidping migrate repair`.

The correct *prevention* is never to edit an applied migration: add a new
numbered one instead, written to be idempotent so it is a no-op on databases
that already got the DDL. (A self-healing migration numbered `014` once did
this for a rewritten 013; it was withdrawn before release in favor of repairing
the few affected dev databases by hand — pre-release, hand repair beats
shipping a permanent migration. `014_v0_17_0` is the consolidated v0.17.0
migration, unrelated to that draft; see the consolidation rule above for why
reusing the number is safe here and what it costs dev databases.)

**The current unreleased target is `018_v0_23_0`.** v0.22.0 shipped, so `017`
is frozen: everything this cycle produces is appended to `018_v0_23_0.up.sql` /
`.down.sql` (both dialects) instead. That number is unreleased, so it costs dev
databases nothing — a database that already ran `017` simply picks `018` up as
the next unapplied migration, with no reset, no hand-apply and no
`solidping migrate repair`. Check the latest `vX.Y.Z` tag before appending to a
migration, not the previous cycle's habit — and **update this paragraph when
you are the one who moves past a frozen number**, because a stale pointer here
is what causes the mistake it is meant to prevent. It has already happened
three times: `015` acquired its first section only after a change had already
been written into `014` on the stale belief that v0.17.0 was still pending;
this paragraph then went on naming `015` after v0.18.0–v0.18.3 had all
shipped; and the file that is now `018_v0_23_0` was first written as
`018_v0_22_0`, named after a version that had already been tagged the same
day.

Note that the number and the version do not advance in lockstep: `017` is
named `v0_21_0` yet it is the migration v0.22.0 shipped with, because v0.22.0
needed no DDL of its own. The version suffix names the release a file ships
IN, so always resolve it from the latest tag plus what is in flight (an open
release-please PR, and whether this cycle carries `feat:` commits and so lands
a minor), never by incrementing the previous file's suffix.

## Development workflow

During a release cycle developers add scratch migrations as needed (e.g., `002_add_foo.up.sql`). At release time:

1. Capture a golden schema (fresh DB + dump) from the scratch migrations.
2. Hand-author `NNN_vX_Y_Z.up.sql` with the **net final DDL** (not a concatenation — omit columns and tables that were added then removed during the cycle).
3. Delete all scratch migrations.
4. Run the new single migration on a fresh DB, dump schema, diff against golden — must match exactly.
5. Run `make test` to confirm both backends pass.

See `.claude/skills/database.md` for the full SQL style guide.

## Advisory-lock keys

Postgres session advisory locks are how SolidPing makes a supervisor run on at
most one replica (the JMAP inbox consumer is the first — spec 2026-08-22-01).
The helper is `server/internal/db/dblock`, and **that package's doc comment is
the authoritative key registry** — this section only points at it and states
the rules.

- Keys are **hand-allocated**, never hashed from a string. A hash collision
  between two unrelated features would show up as one of them mysteriously
  never running; a hand-allocated number cannot collide, and `grep 0x5001…`
  finds every user of it.
- Numbering is `0x5001_0000 + <sequence>`. `0x5001` reads as "SP 01" and
  namespaces our keys away from anything else sharing the database.
- Always the **single-argument bigint** form of `pg_advisory_lock`, so the
  whole key space is one flat registry rather than two overlapping ones.
- **Never reuse a retired number.** During a rolling deploy an old pod may
  still hold the key its version assigned to a removed feature; a new pod
  reusing it for something else would be excluded by a lock that has nothing
  to do with it.
- Take the lock on a **dedicated, long-lived connection**. Session advisory
  locks are released the instant their connection closes, so a lock taken
  through the pool is dropped invisibly the moment that connection is recycled.
  `dblock` pins a `bun.Conn` for the lifetime of the work and pings it, which
  costs one pool slot on the holder — budget for it (see
  `project_solidping_dev_pg_pool_budget`-style pool sizing).
- **A lock is never the correctness mechanism.** Holders lose locks (dead
  connection, paused process) before they notice, so two copies of the work can
  briefly overlap. Whatever runs under `dblock.RunExclusive` must still be safe
  when it does.
- On SQLite there is one process by construction: `dblock` skips the lock and
  runs the work directly.
