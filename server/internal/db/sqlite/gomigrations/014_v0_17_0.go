package gomigrations

import (
	"context"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/migrate"

	"github.com/fclairamb/solidping/server/internal/db/migrationguard"
)

// source013 is the migration 014 re-applies. It is addressed by path rather
// than copied, so the statements 014 runs are BYTE-IDENTICAL to the ones a
// healthy database got from 013 — which is what makes a healed database and a
// freshly migrated one schema-identical. SQLite stores the literal text of an
// added column definition (and of a CREATE TRIGGER) in `sqlite_master`, so a
// re-typed copy with different whitespace would leave two databases that behave
// the same but do not compare equal.
const source013 = "migrations/013_v0_16_0.up.sql"

// Checksum014 is the guard checksum for this migration (see
// internal/db/migrationguard). A Go migration has no file to hash, so its
// identity is declared: bump the trailing revision if the behaviour of the
// migration ever changes, and never otherwise.
const Checksum014 = "go:014_v0_17_0/heal-013-worker-capabilities/1"

// name014 is the numeric prefix bun keys this migration on. It must match this
// file's name; TestMigration014IsRegistered pins that.
const name014 = "014"

// splitDirective is bun's own statement separator inside a .sql migration.
const splitDirective = "--bun:split"

// addColumnPrefix and createTriggerRE recognise the two kinds of statement 013
// is allowed to contain. Anything else makes 014 refuse to guess: a statement
// it cannot reason about is a statement it cannot make idempotent.
const addColumnPrefix = "alter table workers add column capabilities"

var createTriggerRE = regexp.MustCompile(`(?is)^create\s+trigger\s+([a-z0-9_]+)\b`)

// Register adds migration 014 — a self-healing re-application of 013 — to the
// collection.
//
// WHY THIS EXISTS. Migration 013 was rewritten in place after a running dev
// loop had already applied an earlier draft of it. bun keys applied migrations
// on the numeric prefix alone, so the rewritten statements never ran while
// `bun_migrations` kept claiming 013 was applied: the `workers.capabilities`
// column and its two shape triggers were simply missing, every query touching
// them failed, region resolution 404'd and check scheduling stopped (spec
// 2026-08-18-02).
//
// WHY IT IS A GO MIGRATION. SQLite has no `ALTER TABLE ... ADD COLUMN IF NOT
// EXISTS` and no `CREATE TRIGGER` form that both skips an existing trigger and
// stores the same text, so the "apply only what is missing" decision has to be
// made in code, against `pragma_table_info` and `sqlite_master`. The Postgres
// side of 014 is plain idempotent SQL for the same reason reversed.
//
// fsys must be the filesystem carrying the SQLite migration files (the
// `migrations/*.sql` embed in package sqlite).
func Register(migrations *migrate.Migrations, fsys fs.FS) error {
	up := func(ctx context.Context, db *bun.DB) error {
		return heal013(ctx, db, fsys)
	}

	// Down is deliberately a no-op. 014 creates nothing of its own — it only
	// finishes 013 — and 013's own down migration drops the column and the
	// triggers. Undoing 014 on a healthy database would leave the schema
	// broken in exactly the way 014 exists to repair.
	down := func(_ context.Context, _ *bun.DB) error { return nil }

	if err := migrations.Register(up, down); err != nil {
		return fmt.Errorf("register migration %s: %w", name014, err)
	}

	return nil
}

// heal013 applies every statement of 013 that is not already in place.
func heal013(ctx context.Context, db *bun.DB, fsys fs.FS) error {
	statements, err := statements(fsys, source013)
	if err != nil {
		return err
	}

	if len(statements) == 0 {
		return fmt.Errorf("migration %s: %s yielded no statements", name014, source013)
	}

	for _, stmt := range statements {
		apply, err := isMissing(ctx, db, stmt)
		if err != nil {
			return err
		}

		if !apply {
			continue
		}

		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration %s: re-apply %q: %w", name014, firstLine(stmt), err)
		}
	}

	return nil
}

// isMissing reports whether the object stmt creates is absent, and refuses to
// answer for a statement shape it does not recognise.
func isMissing(ctx context.Context, db *bun.DB, stmt string) (bool, error) {
	lower := strings.ToLower(stmt)

	if strings.HasPrefix(lower, addColumnPrefix) {
		exists, err := columnExists(ctx, db, "workers", "capabilities")

		return !exists, err
	}

	if match := createTriggerRE.FindStringSubmatch(stmt); match != nil {
		exists, err := triggerExists(ctx, db, match[1])

		return !exists, err
	}

	return false, fmt.Errorf(
		"migration %s: unrecognised statement in %s: %q — 014 can only re-apply the "+
			"column and the shape triggers, so a new statement needs its own idempotency guard",
		name014, source013, firstLine(stmt),
	)
}

func columnExists(ctx context.Context, db *bun.DB, table, column string) (bool, error) {
	var count int

	err := db.NewRaw(
		"SELECT count(*) FROM pragma_table_info(?) WHERE name = ?", table, column,
	).Scan(ctx, &count)
	if err != nil {
		return false, fmt.Errorf("inspect %s.%s: %w", table, column, err)
	}

	return count > 0, nil
}

func triggerExists(ctx context.Context, db *bun.DB, name string) (bool, error) {
	var count int

	err := db.NewRaw(
		"SELECT count(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?", name,
	).Scan(ctx, &count)
	if err != nil {
		return false, fmt.Errorf("inspect trigger %s: %w", name, err)
	}

	return count > 0, nil
}

// statements reads a bun .sql migration and returns its executable statements,
// split on `--bun:split` with the leading comment block of each chunk removed.
//
// Stripping the leading comments is not cosmetic: SQLite records the text it
// was given, and a column definition or trigger body carrying a comment prefix
// would not compare equal to the one a fresh database got.
func statements(fsys fs.FS, name string) ([]string, error) {
	body, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}

	chunks := strings.Split(string(body), splitDirective)
	out := make([]string, 0, len(chunks))

	for _, chunk := range chunks {
		if stmt := stripLeadingComments(chunk); stmt != "" {
			out = append(out, stmt)
		}
	}

	return out, nil
}

// stripLeadingComments drops the blank and `--` lines that open a chunk, and
// trims the trailing whitespace and semicolon, leaving the bare statement.
func stripLeadingComments(chunk string) string {
	lines := strings.Split(chunk, "\n")

	start := 0
	for start < len(lines) {
		trimmed := strings.TrimSpace(lines[start])
		if trimmed != "" && !strings.HasPrefix(trimmed, "--") {
			break
		}

		start++
	}

	stmt := strings.Join(lines[start:], "\n")
	stmt = strings.TrimSpace(stmt)

	return strings.TrimSuffix(stmt, ";")
}

func firstLine(stmt string) string {
	if idx := strings.IndexByte(stmt, '\n'); idx >= 0 {
		return stmt[:idx]
	}

	return stmt
}

// Guarded returns the identity + declared checksum of every Go migration in
// this package, for internal/db/migrationguard. A Go migration has no file, so
// the guard cannot hash it; declaring the checksum keeps it inside the same
// "an applied migration never changes" invariant.
func Guarded() []migrationguard.Migration {
	return []migrationguard.Migration{
		{Name: name014, Comment: "v0_17_0", Checksum: Checksum014},
	}
}
