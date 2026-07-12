package sqlite

import (
	"io/fs"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestForeignKeysEnforcedOnFreshConnection pins spec 2026-07-12-01 §4: foreign
// keys are enforced on EVERY connection, including ones the pool re-opens (not
// just the one the startup one-shot PRAGMA touched). A file-based DB is used
// because the DSN param only applies there; SetMaxIdleConns(0) forces the read
// and the orphan insert onto genuinely fresh connections.
func TestForeignKeysEnforcedOnFreshConnection(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, err := New(ctx, Config{DataDir: t.TempDir()})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	// Drop idle connections so each statement below lands on a fresh connection,
	// exercising the DSN-level enforcement rather than the one-shot PRAGMA.
	svc.db.SetMaxIdleConns(0)
	defer svc.db.SetMaxIdleConns(1)

	var enabled int
	r.NoError(svc.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&enabled))
	r.Equal(1, enabled, "a fresh connection must enforce foreign keys")

	// And enforcement actually bites: inserting a result for a non-existent check
	// must fail the results.check_uid → checks.uid foreign key.
	orgUID := uuid.NewString()
	_, err = svc.db.NewRaw(
		"INSERT INTO organizations (uid, slug, name) VALUES (?, ?, ?)",
		orgUID, "fk-org", "FK Org",
	).Exec(ctx)
	r.NoError(err)

	_, err = svc.db.NewRaw(
		"INSERT INTO results (uid, organization_uid, check_uid, period_type, period_start, status) "+
			"VALUES (?, ?, ?, 'raw', '2026-07-01 00:00:00', 3)",
		uuid.NewString(), orgUID, uuid.NewString(),
	).Exec(ctx)
	r.Error(err, "inserting an orphan result must be rejected by the foreign key")
}

// TestMigration007PurgesOrphans pins spec 2026-07-12-01 §3: the 007 migration
// deletes results rows whose check no longer exists and leaves live checks'
// rows intact. Idempotent / no-op on clean data.
func TestMigration007PurgesOrphans(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	org := models.NewOrganization("orphan-purge-org", "")
	r.NoError(svc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "live-check", "http")
	r.NoError(svc.CreateCheck(ctx, check)) // seeds one non-orphan 'created' marker

	// Seed several orphan rows (check does not exist) via a connection with FK
	// enforcement switched off — how the production orphans came to be.
	orphanCheckUID := uuid.NewString()
	insertOrphanResults(t, svc, org.UID, orphanCheckUID, 3)

	countByCheck := func(checkUID string) int {
		var n int
		r.NoError(svc.db.NewRaw(
			"SELECT count(*) FROM results WHERE check_uid = ?", checkUID,
		).Scan(ctx, &n))

		return n
	}

	r.Equal(3, countByCheck(orphanCheckUID), "orphans must exist before the migration")
	r.Equal(1, countByCheck(check.UID), "the live check's marker row must exist")

	// Run the real 007 up migration SQL.
	migrationSQL, err := fs.ReadFile(migrationsFS, "migrations/007_v0_5_0.up.sql")
	r.NoError(err)
	_, err = svc.db.ExecContext(ctx, string(migrationSQL))
	r.NoError(err)

	r.Equal(0, countByCheck(orphanCheckUID), "the migration must purge every orphan row")
	r.Equal(1, countByCheck(check.UID), "the live check's rows must be preserved")

	// Idempotent: a second run changes nothing.
	_, err = svc.db.ExecContext(ctx, string(migrationSQL))
	r.NoError(err)
	r.Equal(1, countByCheck(check.UID))
}

// insertOrphanResults inserts n raw results referencing a non-existent check,
// on a single connection with foreign keys switched off so the insert is not
// rejected. The connection is released before returning.
func insertOrphanResults(t *testing.T, svc *Service, orgUID, checkUID string, n int) {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	conn, err := svc.db.Conn(ctx)
	r.NoError(err)
	defer func() { _ = conn.Close() }()

	_, err = conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF")
	r.NoError(err)

	for range n {
		_, err = conn.ExecContext(ctx,
			"INSERT INTO results (uid, organization_uid, check_uid, period_type, period_start, status) "+
				"VALUES (?, ?, ?, 'raw', '2026-07-01 00:00:00', 3)",
			uuid.NewString(), orgUID, checkUID,
		)
		r.NoError(err)
	}

	_, err = conn.ExecContext(ctx, "PRAGMA foreign_keys = ON")
	r.NoError(err)
}
