package postgres

import (
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portMigrateNotRecorded is distinct from every other _postgres_test.go
// embedded port in the repo.
const portMigrateNotRecorded = 15591

// TestFailedMigrationIsNotRecordedAndIsRetried_Postgres is the Postgres twin of
// the SQLite test (spec 2026-10-13-01): a migration that fails half way must not
// be recorded as applied, and runs again once fixed.
//
//nolint:paralleltest // shares dev-machine embedded-postgres resources with its siblings
func TestFailedMigrationIsNotRecordedAndIsRetried_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portMigrateNotRecorded, RunMode: runModeTest})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = svc.Close() })

	if initErr := svc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	t.Cleanup(func() {
		_, _ = svc.DB().ExecContext(ctx, "drop table if exists spec_20261013_extras")
		_, _ = svc.DB().ExecContext(ctx, "drop table if exists spec_20261013_things")
	})

	// The shipped migrations plus one test migration appended after them.
	withExtra := func(second string) fs.FS {
		fsys := fstest.MapFS{}

		r.NoError(fs.WalkDir(migrationsFS, "migrations", func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return walkErr
			}

			data, readErr := fs.ReadFile(migrationsFS, path)
			fsys[path] = &fstest.MapFile{Data: data}

			return readErr
		}))

		fsys["migrations/900_test_spec_20261013.up.sql"] = &fstest.MapFile{Data: []byte(second)}

		return fsys
	}

	const (
		broken = "create table if not exists spec_20261013_extras (uid text);\n" +
			"--bun:split\ninsert into spec_20261013_no_such_table values (1);"
		fixed = "create table if not exists spec_20261013_extras (uid text);\n" +
			"--bun:split\ncreate table if not exists spec_20261013_things (uid text);"
	)

	recorded := func() int {
		t.Helper()

		var count int

		r.NoError(svc.DB().QueryRowContext(ctx,
			"select count(*) from bun_migrations where name = '900'").Scan(&count))

		return count
	}

	svc.migrations = withExtra(broken)
	initErr := svc.Initialize(ctx)
	r.Error(initErr)
	r.Contains(initErr.Error(), "900")
	r.Zero(recorded(), "a migration that failed half way must NOT be recorded as applied")

	svc.migrations = withExtra(fixed)
	r.NoError(svc.Initialize(ctx), "the fixed migration is retried")
	r.Equal(1, recorded())
}
