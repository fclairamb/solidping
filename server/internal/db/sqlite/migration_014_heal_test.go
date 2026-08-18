package sqlite

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun/migrate"

	"github.com/fclairamb/solidping/server/internal/db/dbcaptest"
	"github.com/fclairamb/solidping/server/internal/db/sqlite/gomigrations"
)

// schemaObjects returns the raw `sqlite_master.sql` of the workers table and of
// its two capability shape triggers.
//
// Comparing the STORED TEXT, not merely "does the column exist", is the whole
// point: SQLite records the literal text of an added column definition and of a
// CREATE TRIGGER body, so two databases can both have a `capabilities` column
// and still disagree about its CHECK constraint. Only text equality proves a
// healed database and a fresh one are the same database.
func schemaObjects(ctx context.Context, t *testing.T, svc *Service) map[string]string {
	t.Helper()

	type row struct {
		Name string `bun:"name"`
		SQL  string `bun:"sql"`
	}

	var rows []row

	require.NoError(t, svc.db.NewRaw(
		"SELECT name, sql FROM sqlite_master "+
			"WHERE name = 'workers' OR name LIKE 'workers_capabilities%' ORDER BY name",
	).Scan(ctx, &rows))

	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Name] = r.SQL
	}

	return out
}

// desyncWorkerCapabilities reproduces the incident of spec 2026-08-18-02
// exactly: the DDL migration 013 was supposed to leave behind is gone, while
// `bun_migrations` still claims 013 was applied — so bun will never re-run it.
//
// It also removes 014's own bookkeeping, because the scenario being modeled is
// a database that predates 014 entirely.
func desyncWorkerCapabilities(ctx context.Context, t *testing.T, svc *Service) {
	t.Helper()

	for _, stmt := range []string{
		"drop trigger workers_capabilities_shape_update",
		"drop trigger workers_capabilities_shape_insert",
		"alter table workers drop column capabilities",
		"delete from bun_migrations where name = '014'",
		"delete from migration_checksums where name = '014'",
	} {
		_, err := svc.db.ExecContext(ctx, stmt)
		require.NoError(t, err, "desync step %q must apply", stmt)
	}
}

// TestMigration014HealsDesyncedWorkerCapabilities is THE regression guard for
// spec 2026-08-18-02: after 014, a database that silently skipped 013's
// rewritten content must be schema-identical to one that applied it.
func TestMigration014HealsDesyncedWorkerCapabilities(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	fresh, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = fresh.Close() })
	r.NoError(fresh.Initialize(ctx))

	desynced, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = desynced.Close() })
	r.NoError(desynced.Initialize(ctx))

	desyncWorkerCapabilities(ctx, t, desynced)

	// Negative control: the reproduction really is broken before 014 runs.
	broken := schemaObjects(ctx, t, desynced)
	r.NotContains(broken, "workers_capabilities_shape_insert",
		"the reproduction must actually be missing the triggers")
	r.NotContains(broken["workers"], "capabilities",
		"the reproduction must actually be missing the column")

	// Booting again is all it takes.
	r.NoError(desynced.Initialize(ctx), "the fixed server must boot against a desynced database")

	healed := schemaObjects(ctx, t, desynced)
	r.Equal(schemaObjects(ctx, t, fresh), healed,
		"after 014 a healed database and a freshly migrated one must be schema-identical, "+
			"down to the stored DDL text")
	r.Contains(healed, "workers_capabilities_shape_insert")
	r.Contains(healed, "workers_capabilities_shape_update")
	r.Contains(healed["workers"], "capabilities")

	// 014 must be recorded as applied, so it does not re-run every boot.
	var applied int
	r.NoError(desynced.db.NewRaw(
		"SELECT count(*) FROM bun_migrations WHERE name = '014'",
	).Scan(ctx, &applied))
	r.Equal(1, applied, "014 must be recorded in bun_migrations")
}

// TestMigration014RestoresConstraintVerdicts proves the healed column enforces
// the shape rules, not just that a column named `capabilities` exists. A heal
// that dropped the CHECK and the triggers would pass a column-presence
// assertion and still let garbage into the table.
func TestMigration014RestoresConstraintVerdicts(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	desyncWorkerCapabilities(ctx, t, svc)
	r.NoError(svc.Initialize(ctx))

	cases := append(dbcaptest.SharedCases(), dbcaptest.SQLiteOnlyCases()...)
	r.NotEmpty(cases)

	for i, tc := range cases {
		slug := fmt.Sprintf("healed-%d", i)
		_, err := svc.db.ExecContext(ctx,
			fmt.Sprintf(
				"INSERT INTO workers (uid, slug, name, capabilities) VALUES ('%s', '%s', '%s', %s)",
				slug, slug, slug, tc.SQLiteLiteral,
			),
		)

		if tc.Accepted {
			r.NoError(err, "case %q must be accepted after the heal", tc.Name)
		} else {
			r.Error(err, "case %q must still be rejected after the heal", tc.Name)
		}
	}
}

// TestMigration014IsANoopOnAHealthyDatabase is the positive control: running
// the heal against a database that never desynced must change nothing. A 014
// that unconditionally re-applied 013 would fail here with "duplicate column".
func TestMigration014IsANoopOnAHealthyDatabase(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	before := schemaObjects(ctx, t, svc)

	// Force 014 to run again on an already-healthy schema.
	_, err = svc.db.ExecContext(ctx, "delete from bun_migrations where name = '014'")
	r.NoError(err)
	_, err = svc.db.ExecContext(ctx, "delete from migration_checksums where name = '014'")
	r.NoError(err)

	r.NoError(svc.Initialize(ctx), "014 must be a no-op on a healthy database")
	r.Equal(before, schemaObjects(ctx, t, svc))
}

// TestMigration014RegisteredIdentity pins the name and comment bun derives from
// the Go migration's FILE NAME, and that the guard's declared identity agrees
// with it. Renaming 014_v0_17_0.go silently changes the migration's identity —
// bun would treat it as a different, unapplied migration — which is the exact
// class of accident this whole spec is about.
func TestMigration014RegisteredIdentity(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	registered := migrate.NewMigrations()
	r.NoError(gomigrations.Register(registered, migrationsFS))

	sorted := registered.Sorted()
	r.Len(sorted, 1, "the package must register exactly one migration")
	r.Equal("014", sorted[0].Name, "bun derives the name from the .go file name")
	r.Equal("v0_17_0", sorted[0].Comment, "bun derives the comment from the .go file name")

	guarded := gomigrations.Guarded()
	r.Len(guarded, 1)
	r.Equal(sorted[0].Name, guarded[0].Name,
		"the guard must key the Go migration exactly as bun does")
	r.Equal(sorted[0].Comment, guarded[0].Comment)
	r.NotEmpty(guarded[0].Checksum)
}

// TestMigration014IsDiscoveredAlongsideTheSQLMigrations proves the Go migration
// takes part in the same ordered list as the .sql ones — a 014 that never made
// it into the collection would heal nothing while every test above still
// passed, because Initialize is what wires it in.
func TestMigration014IsDiscoveredAlongsideTheSQLMigrations(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	all := migrate.NewMigrations()
	r.NoError(all.Discover(migrationsFS))
	r.NoError(gomigrations.Register(all, migrationsFS))

	sorted := all.Sorted()
	names := make([]string, 0, len(sorted))

	for _, m := range sorted {
		names = append(names, m.Name)
	}

	r.Contains(names, "013")
	r.Contains(names, "014")
	r.Equal("014", names[len(names)-1], "014 must sort last, after 013")
}
