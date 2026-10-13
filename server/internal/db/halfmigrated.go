package db

import (
	"errors"
	"fmt"
	"strings"
)

// ErrHalfMigrated marks a startup error that looks like the database schema is
// behind what the binary expects even though the migrator reported nothing
// pending. Before spec 2026-10-13-01, bun recorded a migration as applied
// before running it, so a process killed (or a migration failing) half way left
// a database claiming a schema it does not have.
var ErrHalfMigrated = errors.New("the database looks half-migrated")

// DescribeHalfMigrated turns a bare "no such column" / "no such table" (SQLite)
// or "does not exist" (PostgreSQL) failure raised once migrations have run into
// an error that says what probably happened and what to do. It only rewords the
// error: it never tries to repair a database in an unknown state. Any other
// error, and nil, come back unchanged.
func DescribeHalfMigrated(err error) error {
	if err == nil || !looksHalfMigrated(err.Error()) {
		return err
	}

	return fmt.Errorf("%w: a migration was probably interrupted or failed on an older version "+
		"and was recorded as applied without its schema changes. Restore the "+
		"'<database>.pre-<migration>.bak' file next to the database if there is one, or recreate the database "+
		"(see wiki/runbooks/operations-gotchas.md, 'Half-migrated database'): %w", ErrHalfMigrated, err)
}

func looksHalfMigrated(msg string) bool {
	msg = strings.ToLower(msg)

	if strings.Contains(msg, "no such column") || strings.Contains(msg, "no such table") {
		return true
	}

	// PostgreSQL: column "x" of relation "y" does not exist, relation "y" does not exist.
	return strings.Contains(msg, "does not exist") &&
		(strings.Contains(msg, "column ") || strings.Contains(msg, "relation "))
}
