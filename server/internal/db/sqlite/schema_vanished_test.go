package sqlite

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/dbfault"
)

// TestSchemaVanishesWhenDatabaseFileIsDeleted is the reproduction of the
// seventeen-hour incident in which a live server logged
// `SQL logic error: no such table: jobs (1)` on every job query until the host
// disk filled up (spec 2026-08-12-05; narrative in
// wiki/conventions/database-faults.md).
//
// The mechanism, and the reason it looks like nothing is wrong at first:
//
//  1. something deletes the database file out from under the running process
//     (an e2e teardown, a second server booting with SP_DB_RESET=true against
//     the same directory, a temp-directory sweeper);
//  2. the connection already open keeps working — it holds the unlinked inode,
//     so queries succeed and no error is logged;
//  3. the moment the pool has to open a *fresh* connection, the DSN's
//     `mode=rwc` silently creates a brand-new, empty database at that path;
//  4. from then on every query fails with "no such table", forever. Migrations
//     only run at startup, so no retry inside this process can ever repair it.
//
// The test asserts the failure is (a) exactly the incident's error and (b)
// classified as structural, which is what makes the process shut down instead
// of spinning.
func TestSchemaVanishesWhenDatabaseFileIsDeleted(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	dir := t.TempDir()

	svc, err := New(ctx, Config{DataDir: dir, RunMode: "test"})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	countJobs := func() error {
		var n int

		return svc.db.QueryRowContext(ctx, "SELECT count(*) FROM jobs").Scan(&n)
	}

	// Counts user tables in whatever database currently lives at dbPath. Used
	// twice on purpose: once here as a positive control, once at the end. A
	// bare "zero tables" assertion proves nothing unless the same query is
	// shown returning non-zero first.
	countUserTables := func() int {
		var n int

		r.NoError(svc.db.QueryRowContext(ctx,
			"SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'",
		).Scan(&n))

		return n
	}

	r.NoError(countJobs(), "baseline: the migrated schema answers")
	r.Positive(countUserTables(), "baseline: the migrated schema has tables")

	// Step 1: the file (and its WAL sidecars, if any) vanish.
	dbPath := filepath.Join(dir, "solidping-test.db")
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if _, statErr := os.Stat(path); statErr == nil {
			r.NoError(os.Remove(path))
		}
	}

	_, err = os.Stat(dbPath)
	r.True(os.IsNotExist(err), "the database file is gone")

	// Step 2: the connection already open still works. This is why the fault is
	// invisible until some later, unrelated moment.
	r.NoError(countJobs(), "an already-open connection keeps serving the unlinked inode")

	// Step 3: force the pool to open a fresh connection, which is what happens
	// on its own when a connection is discarded after an error or reaped.
	svc.db.SetMaxIdleConns(0)

	// Step 4: permanent, identical failure.
	for attempt := range 3 {
		err = countJobs()
		r.Error(err, "attempt %d", attempt)
		r.Contains(err.Error(), "no such table: jobs",
			"attempt %d: the incident's exact symptom", attempt)

		fault := dbfault.Describe(err)
		r.NotNil(fault, "attempt %d: the driver error must classify as structural", attempt)
		r.Equal(dbfault.BackendSQLite, fault.Backend)
		r.Equal("undefined_table", fault.Reason)
	}

	// And the smoking gun: mode=rwc recreated a database in place, so nothing
	// at the filesystem level looks broken either.
	_, statErr := os.Stat(dbPath)
	r.NoError(statErr, "the DSN recreated the database file")

	// …and it carries no schema: the tables are not coming back.
	//
	// Asserted against sqlite_master rather than the file's size. How many
	// bytes a freshly created database occupies is a driver implementation
	// detail — modernc.org/sqlite v1.58 writes the 4096-byte page header
	// eagerly where v1.52 left a zero-byte file — so the old
	// `r.Zero(info.Size())` failed on a routine dependency bump even though
	// the behavior this test documents had not changed at all. The claim
	// worth pinning is that the database has no tables, which is precisely
	// what makes the "no such table" failure above permanent.
	r.Zero(countUserTables(), "the recreated database carries no schema")
}
