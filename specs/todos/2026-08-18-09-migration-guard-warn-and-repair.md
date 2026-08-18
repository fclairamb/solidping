---
model: sonnet
effort: high
---

# Migration guard: configurable warn-only mode (blocking stays the default) and a `migrate repair` command

## Problem

The migration guard (`server/internal/db/migrationguard/migrationguard.go`) fails
startup whenever an applied migration's `.up.sql` content no longer matches the
checksum recorded at apply time. That is the right default for silent schema
divergence (it exists because of spec 2026-08-18-02 and the earlier
migration-consolidation desync), but in day-to-day development it is too blunt:

- Editing a **comment** (or whitespace) inside an already-applied migration
  bricks every local database that ran the earlier version — the app refuses to
  boot for an edit that changes nothing about the schema.
- The only repair paths are a dev-DB reset or hand-pasting the `UPDATE
  migration_checksums …` statement printed in the error message, once per
  affected database.

Decision (owner): **blocking stays the default behavior.** Add a
setting/environment variable that switches the guard to warn-only, and make the
local dev environment always run in warn-only mode. Independently, add a repair
command so re-blessing the current files is a one-command operation.

## Proposal

Three changes, both dialects (`server/internal/db/sqlite/` and
`server/internal/db/postgres/`):

### 1. A guard mode setting: `strict` (default) | `warn`

- New config key, koanf path `db.migration_guard_mode` (or the equivalent under
  the existing DB config section — follow the config struct's layout), env
  `SP_DB_MIGRATION_GUARD_MODE`, values `strict` | `warn`, default `strict`.
  Any other value is a config validation error. **Multi-word koanf key**: it
  must be registered in the manual SP_* env reader (same quirk as
  `rate_limiting` / `shutdown_timeout`, cf. `internal/config/`), otherwise the
  env var silently won't map.
- Thread the mode into both `sqlite.Service` and `postgres.Service` the same
  way `reset`/`runMode` already travel.

**Behavior per mode** (`Initialize`, sqlite.go ~:267 and postgres.go ~:258,
which call `Reconcile` before and after `Migrate`):

- `strict`: exactly today's behavior. Mismatch → startup fails with the joined
  `MismatchError`s, and nothing is written to `migration_checksums` on a
  diverged database ("report drift before writing anything" stays).
- `warn`: mismatches are logged with `slog.WarnContext` — one warning per
  mismatched migration, carrying the full `MismatchError` text — and boot
  continues. Log from the **first** Reconcile call only; the second call sees
  the same set and must not double-log. Two invariants in warn mode:
  - Rows with **no recorded checksum** (first boot on a pre-guard database)
    are still backfilled even when other rows mismatch.
  - The recorded checksum of a **mismatched row is never overwritten** — the
    stale record is what makes the warning recur on every boot until someone
    repairs deliberately.

Implementation latitude: either pass the mode into the guard, or have
`Reconcile` return the mismatch list as data (`([]*MismatchError, error)`, with
`error` reserved for real I/O/SQL failures) and let `Initialize` apply the
policy — the latter keeps the guard policy-free. Keep `ErrChecksumMismatch`
and `MismatchError` (wrapping unchanged) so tests and callers can classify.
Update the package doc comment (migrationguard.go:1-22) to describe both modes.

### 2. Local dev always runs warn-only

- Set `SP_DB_MIGRATION_GUARD_MODE=warn` in the local dev loop so `make dev`,
  `make dev-test`, and `make dev-saas` (the devloop supervisor) always run
  warn-only. Deployed/production defaults remain `strict` — nothing to change
  there.

### 3. `solidping migrate repair` subcommand

- Add a `repair` subcommand nested under the existing `migrate` command in
  `server/main.go` (:53-57; urfave/cli v3 supports `Commands` alongside
  `Action`). Usage: "Re-record checksums for applied migrations after a
  deliberate edit (comment/whitespace changes, or after applying missing
  statements by hand)".
- Implement `Guard.Repair(ctx) ([]RepairResult, error)` in migrationguard:
  ensure the side table, read applied names + recorded checksums, and for every
  applied migration known to the binary either insert the missing row or update
  a mismatched row to the current file checksum (also refresh `comment` and
  `recorded_at`). Return what changed: `{Name, Comment, Old, New}` with `Old`
  empty for a backfill. Applied migrations the binary no longer ships are left
  alone (same tolerance as Reconcile).
- Repair must **not** run migrations — it opens the DB (same config/open path
  as the other CLI commands, cf. `openDB` used by `encryptCredentials` in
  main.go:394) and only touches `migration_checksums`. Log one line per
  repaired migration (name, old→new short checksums) and a summary count;
  "nothing to repair" exits 0.
- Expose it through the db service layer the CLI uses: both `sqlite.Service`
  and `postgres.Service` get a small method (e.g. `RepairMigrationChecksums`)
  building the guard over their embedded `migrationsFS` (sqlite already has
  `newMigrationGuard`, sqlite.go:300 — mirror it in postgres), plus whatever
  addition the shared `db.Service` interface needs so `main.go` can call it
  backend-agnostically.

### 4. Message + docs follow-through

- Rewrite the `MismatchError.Error()` repair guidance (migrationguard.go:80-91):
  option (1) cosmetic edit → `solidping migrate repair`; (2) development →
  reset (`SP_DB_RESET=true` in test/demo, or delete the SQLite file) or run
  with `SP_DB_MIGRATION_GUARD_MODE=warn`; (3) otherwise → apply the missing
  statements by hand, then `solidping migrate repair`. Drop the raw `UPDATE`
  from the message.
- Update `wiki/conventions/database.md` and the migration-naming section of
  `server/CLAUDE.md` where they describe the guard, documenting the mode
  setting and the repair command.

### Tests

- Strict mode (default): existing guard tests keep passing unchanged in intent
  — a content edit after apply still fails boot, and nothing is written to
  `migration_checksums` on the diverged database.
- Warn mode: the same edit boots successfully, surfaces the mismatch list
  (warn-logged), and — backfill-during-mismatch — a pre-guard DB with one
  edited migration gets the other rows recorded while the edited row keeps its
  recorded checksum.
- Config: default resolves to `strict`; invalid value fails validation;
  `SP_DB_MIGRATION_GUARD_MODE` maps through the manual env reader.
- `Repair`: on a diverged DB updates only the mismatched/missing rows and
  reports them; a clean DB reports nothing; a subsequent `Reconcile` returns no
  mismatches (boot is clean in strict mode again, warning stops in warn mode).
- CLI-level: `migrate repair` wiring compiles for both dialects (table-driven
  service-level test is enough; no need to shell out to the binary).

### Non-goals / accepted trade-off

In warn mode a genuinely missing DDL (the 2026-08-18-02 scenario) boots and
limps — accepted deliberately for local dev only, where the recurring WARN plus
the one-command repair is the mitigation; production keeps the fatal default.
Do not add content normalization (comment-stripping before hashing): judging
"comment-only" safely needs a SQL parser, and a regex strip would punch holes
in the guard.

## Implementation Plan

1. **`migrationguard` package** (`server/internal/db/migrationguard/migrationguard.go`):
   - Add `type Mode string` with `ModeStrict`/`ModeWarn` constants.
   - Change `Reconcile(ctx) error` → `Reconcile(ctx, mode Mode) ([]*MismatchError, error)`.
     `error` is reserved for I/O/SQL failures; mismatches are returned as data. Strict
     mode keeps "report drift before writing anything" (no write at all while any
     mismatch exists); warn mode always writes newly-discovered (`!seen`) rows even
     while others mismatch. A mismatched row is never in the write set either way, so
     it is never overwritten by either mode.
   - Add `(Mode) Apply(mismatches) error` — strict turns a non-empty list into the
     joined boot-failing error, warn always returns nil.
   - Add `LogMismatches(ctx, mismatches)` — warn-logs each mismatch once; callers must
     invoke it only after the FIRST Reconcile call in a boot sequence.
   - Add `RepairResult{Name, Comment, Old, New}` and `(*Guard) Repair(ctx) ([]RepairResult, error)`:
     ensures the table, then for every applied+known migration either inserts a missing
     row (`Old` empty) or updates a mismatched row's checksum+comment+recorded_at
     (`Old` = previous checksum). Unshipped migrations are skipped, same as Reconcile.
   - Rewrite `MismatchError.Error()` per spec §4 (repair options 1/2/3), keeping the
     literal substrings the existing tests pin (`"reset the database"`, `ChecksumTable`,
     the short current-checksum form) so those tests need no changes.
   - Update the package doc comment to describe both modes.

2. **Config** (`server/internal/config/config.go`, `envvars.go`):
   - `DatabaseConfig.MigrationGuardMode string \`koanf:"migration_guard_mode"\``, default
     `"strict"` in the defaults literal.
   - `ErrInvalidMigrationGuardMode`; `validateDatabaseConfig` rejects anything other than
     `strict`/`warn`.
   - `applyMigrationGuardModeEnv` reads `SP_DB_MIGRATION_GUARD_MODE` by hand (multi-word
     koanf key), called from `Load()` alongside `applyDBSlowQueryEnv`.
   - Register `SP_DB_MIGRATION_GUARD_MODE` in `manualReaderPlatformEnvVars()`.

3. **Thread the mode into both engines**, mirroring how `reset`/`runMode` already travel:
   - `sqlite.Config.GuardMode` / `postgres.Config.GuardMode migrationguard.Mode`; each
     `Service` gets a `guardMode` field set in `New` (postgres: also set on the `Service`
     returned by the embedded branch, without changing `NewEmbedded`'s signature — that
     function has 4 other test callers that all want the strict default, which the zero
     value already provides).
   - `Initialize` in both engines: call `Reconcile` before `Migrate`, apply the mode
     policy, log mismatches (first call only), migrate, call `Reconcile` again, apply
     policy again (no log). Postgres gets a `newMigrationGuard` helper mirroring
     sqlite's, used by both `Initialize` and the new repair method.
   - `internal/app/server.go` `NewServer` and `main.go` (`openDB`, `runMigrations`) pass
     `migrationguard.Mode(cfg.Database.MigrationGuardMode)` through.

4. **`migrate repair` CLI**:
   - `db.Service` interface gets `RepairMigrationChecksums(ctx) ([]migrationguard.RepairResult, error)`;
     both engines implement it via their `newMigrationGuard` helper + `Guard.Repair`.
   - `main.go`: nest a `repair` subcommand under `migrate` (`Commands` alongside the
     existing `Action`). It loads/validates config, opens the DB via `openDB` (same path
     as `encrypt-credentials`) but does **not** call `Initialize` — repair must not run
     migrations — then calls `RepairMigrationChecksums`, logs one line per change
     (name, old→new short checksums) and a summary count, "nothing to repair" exits 0.

5. **Local dev warn-only**: add `SP_DB_MIGRATION_GUARD_MODE=warn` to the `dev`,
   `dev-test`, and `dev-saas` targets in the root `Makefile`.

6. **Docs**: update `wiki/conventions/database.md` and the migration-naming section of
   `server/CLAUDE.md` to describe the mode setting and `migrate repair`.

7. **Tests**:
   - `migrationguard` package: `Reconcile` strict-vs-warn pair on the same
     rewritten-migration fixture (strict fails + writes nothing; warn boots, returns the
     mismatch, and still backfills unrelated missing rows); `Repair` fixes a diverged
     row and reports it, is a no-op on a clean DB, and a following `Reconcile` in strict
     mode is clean again.
   - `sqlite`/`postgres` package tests: existing `TestMigrationGuardFailsBootOnRewrittenMigration`
     and `TestMigrationGuardBackfillsExistingDatabases` keep passing unchanged; add a
     warn-mode boot-succeeds test against the same tampered-row setup (the positive
     control paired with the existing failing test), and a `RepairMigrationChecksums`
     service-level test.
   - Config tests: default resolves to strict; invalid value fails validation; env var
     maps through the manual reader (`TestManualReaderEnvVarsBind` coverage).
