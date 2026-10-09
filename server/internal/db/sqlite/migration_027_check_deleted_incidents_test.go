package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/sqlitedriver"
)

// TestMigration027ResolvesOrphanIncidents proves the check-deleted-incidents
// section of 027 (spec 2026-10-08-02) on a real pre-027 database: every other
// test runs against a fresh schema, where the backfill matches nothing.
//
//	orphan:      active incident on a soft-deleted check → resolved as of
//	             the check's deleted_at, resolution_type check_deleted
//	live:        active incident on a live check → untouched
//	done:        already-resolved incident on a deleted check → untouched
//	tombstoned:  soft-deleted active incident on a deleted check → untouched
func TestMigration027ResolvesOrphanIncidents(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := context.Background()

	database, err := sql.Open(sqlitedriver.Name, ":memory:")
	r.NoError(err)
	t.Cleanup(func() { _ = database.Close() })
	database.SetMaxOpenConns(1)

	_, err = database.ExecContext(ctx, "PRAGMA foreign_keys = ON")
	r.NoError(err)

	for _, name := range append(migrationsBefore024(),
		"024_v0_33_0.up.sql", "025_v0_34_0.up.sql", "026_v0_38_0.up.sql") {
		execMigrationFile(ctx, t, database, name)
	}

	const deletedAt = "2026-09-07 19:32:00"

	stmts := []string{
		`insert into organizations (uid, slug, name) values ('org-1', 'acme', 'Acme')`,
		`insert into checks (uid, organization_uid, slug, type, config, deleted_at)
		 values ('chk-gone', 'org-1', 'gone', 'http', '{}', '` + deletedAt + `')`,
		`insert into checks (uid, organization_uid, slug, type, config)
		 values ('chk-live', 'org-1', 'live', 'http', '{}')`,
		`insert into incidents (uid, organization_uid, check_uid, number, state, started_at)
		 values ('inc-orphan', 'org-1', 'chk-gone', 1, 1, '2026-09-07 18:12:00')`,
		`insert into incidents (uid, organization_uid, check_uid, number, state, started_at)
		 values ('inc-live', 'org-1', 'chk-live', 2, 1, '2026-09-07 18:12:00')`,
		`insert into incidents (uid, organization_uid, check_uid, number, state, started_at, resolved_at, resolution_type)
		 values ('inc-done', 'org-1', 'chk-gone', 3, 2, '2026-09-07 17:00:00', '2026-09-07 17:30:00', 'auto')`,
		`insert into incidents (uid, organization_uid, check_uid, number, state, started_at, deleted_at)
		 values ('inc-tomb', 'org-1', 'chk-gone', 4, 1, '2026-09-07 16:00:00', '2026-09-07 16:30:00')`,
	}
	for _, stmt := range stmts {
		_, err = database.ExecContext(ctx, stmt)
		r.NoError(err, stmt)
	}

	execMigrationFile(ctx, t, database, "027_v0_39_0.up.sql")

	type row struct {
		state          int
		resolvedAt     sql.NullString
		resolutionType sql.NullString
	}

	load := func(uid string) row {
		var got row
		r.NoError(database.QueryRowContext(ctx,
			`select state, resolved_at, resolution_type from incidents where uid = ?`, uid,
		).Scan(&got.state, &got.resolvedAt, &got.resolutionType))

		return got
	}

	orphan := load("inc-orphan")
	r.Equal(2, orphan.state, "an active incident on a deleted check must be resolved")
	r.Equal(deletedAt, orphan.resolvedAt.String, "resolved as of the check's deletion")
	r.Equal("check_deleted", orphan.resolutionType.String)

	live := load("inc-live")
	r.Equal(1, live.state, "an incident on a live check is not an orphan")
	r.False(live.resolvedAt.Valid)

	done := load("inc-done")
	r.Equal("auto", done.resolutionType.String, "an already-resolved incident keeps its resolution")
	r.Equal("2026-09-07 17:30:00", done.resolvedAt.String)

	tomb := load("inc-tomb")
	r.Equal(1, tomb.state, "a soft-deleted incident is not touched")

	// Re-runnable: a second pass finds nothing left to resolve.
	execMigrationFile(ctx, t, database, "027_v0_39_0.up.sql")
	r.Equal(orphan, load("inc-orphan"))
}
