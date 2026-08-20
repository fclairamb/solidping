package migrationguard_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/driver/sqliteshim"

	"github.com/fclairamb/solidping/server/internal/db/migrationguard"
)

func sum(body string) string {
	h := sha256.Sum256([]byte(body))

	return hex.EncodeToString(h[:])
}

// TestChecksumsKeysOnTheNumericPrefix pins the guard's identity model to bun's:
// bun keys an applied migration on the numeric prefix alone, so the guard must
// too — otherwise a renamed-but-not-renumbered file would look like drift, and
// a renumbered one would look like a new migration.
func TestChecksumsKeysOnTheNumericPrefix(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fsys := fstest.MapFS{
		"migrations/001_v0_1_0.up.sql":   {Data: []byte("create table a();")},
		"migrations/001_v0_1_0.down.sql": {Data: []byte("drop table a;")},
		"migrations/013_v0_16_0.up.sql":  {Data: []byte("alter table b add column c;")},
	}

	got, err := migrationguard.Checksums(fsys, "migrations")
	r.NoError(err)
	r.Len(got, 2, "one entry per up migration, keyed by prefix")

	r.Equal("001", got["001"].Name)
	r.Equal("v0_1_0", got["001"].Comment)
	r.Equal(sum("create table a();"), got["001"].Checksum)

	r.Equal("v0_16_0", got["013"].Comment)
	r.Equal(sum("alter table b add column c;"), got["013"].Checksum)
}

// TestChecksumsIgnoreDownMigrations proves a `.down.sql` edit cannot trip the
// guard. A down file never runs during a forward boot, so it cannot desync an
// applied schema, and folding it into the identity would make harmless teardown
// edits fail every deployment.
func TestChecksumsIgnoreDownMigrations(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	before, err := migrationguard.Checksums(fstest.MapFS{
		"migrations/007_v0_6_0.up.sql":   {Data: []byte("select 1;")},
		"migrations/007_v0_6_0.down.sql": {Data: []byte("select 2;")},
	}, "migrations")
	r.NoError(err)

	after, err := migrationguard.Checksums(fstest.MapFS{
		"migrations/007_v0_6_0.up.sql":   {Data: []byte("select 1;")},
		"migrations/007_v0_6_0.down.sql": {Data: []byte("-- rewritten entirely\nselect 99;")},
	}, "migrations")
	r.NoError(err)

	r.Equal(before, after)
}

// TestChecksumsChangeWithContent is the sensitivity control: without this, a
// Checksums that returned a constant would satisfy every other test here.
func TestChecksumsChangeWithContent(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	base, err := migrationguard.Checksums(fstest.MapFS{
		"migrations/013_v0_16_0.up.sql": {Data: []byte("alter table workers add column egress_ipv4 boolean;")},
	}, "migrations")
	r.NoError(err)

	rewritten, err := migrationguard.Checksums(fstest.MapFS{
		"migrations/013_v0_16_0.up.sql": {Data: []byte("alter table workers add column capabilities text;")},
	}, "migrations")
	r.NoError(err)

	r.NotEqual(base["013"].Checksum, rewritten["013"].Checksum,
		"rewriting a migration in place must change its checksum — that is the whole guard")
}

// TestMismatchErrorIsActionable pins the operator-facing contract: the message
// names the migration and both repair routes, and the sentinel is matchable.
func TestMismatchErrorIsActionable(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	err := &migrationguard.MismatchError{
		Name: "013", Comment: "v0_16_0",
		Recorded: "aaaaaaaaaaaaaaaa", Current: "bbbbbbbbbbbbbbbb",
	}

	r.ErrorIs(err, migrationguard.ErrChecksumMismatch)
	r.Contains(err.Error(), "013_v0_16_0")
	r.Contains(err.Error(), "reset the database")
	r.Contains(err.Error(), migrationguard.ChecksumTable)
	r.Contains(err.Error(), "bbbbbbbbbbbb", "the current checksum must be quotable into the repair SQL")
}

// newGuardTestDB builds an in-memory bun.DB seeded with a minimal
// bun_migrations table (only the "name" column Guard reads) listing applied,
// plus a migration_checksums table pre-populated with recorded — one entry
// per already-recorded checksum. A name in applied but absent from recorded
// models a row Guard must backfill; a name in both with a different value
// models drift.
func newGuardTestDB(t *testing.T, applied []string, recorded map[string]string) *bun.DB {
	t.Helper()

	ctx := context.Background()

	sqldb, err := sql.Open(sqliteshim.ShimName, ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqldb.Close() })

	db := bun.NewDB(sqldb, sqlitedialect.New())

	_, err = db.ExecContext(ctx, "CREATE TABLE bun_migrations (name TEXT)")
	require.NoError(t, err)

	for _, name := range applied {
		_, err = db.ExecContext(ctx, "INSERT INTO bun_migrations (name) VALUES (?)", name)
		require.NoError(t, err)
	}

	_, err = db.ExecContext(ctx,
		"CREATE TABLE migration_checksums "+
			"(name TEXT PRIMARY KEY, comment TEXT, checksum TEXT NOT NULL, recorded_at TIMESTAMP)")
	require.NoError(t, err)

	for name, checksum := range recorded {
		_, err = db.ExecContext(ctx,
			"INSERT INTO migration_checksums (name, comment, checksum, recorded_at) "+
				"VALUES (?, '', ?, CURRENT_TIMESTAMP)",
			name, checksum)
		require.NoError(t, err)
	}

	return db
}

// recordedChecksum reads back the checksum recorded for name, and whether a
// row exists at all.
func recordedChecksum(t *testing.T, db *bun.DB, name string) (string, bool) {
	t.Helper()

	var sum string

	err := db.NewRaw("SELECT checksum FROM migration_checksums WHERE name = ?", name).
		Scan(context.Background(), &sum)
	if err != nil {
		return "", false
	}

	return sum, true
}

// TestReconcileStrictVsWarnMismatchPair is the pair the guard's mode support
// exists for: the identical diverged database, reconciled once in each mode.
// Strict must fail the boot and write nothing at all (not even for the
// unrelated backfill-eligible row) — proving the pre-existing behavior is
// unchanged. Warn must let the boot continue (Apply returns nil) while still
// backfilling the unrelated row, and in both modes the mismatched row's own
// checksum must never be touched — that staleness is what makes the warning
// recur until an operator runs `migrate repair`.
func TestReconcileStrictVsWarnMismatchPair(t *testing.T) {
	t.Parallel()

	expected := map[string]migrationguard.Migration{
		"001": {Name: "001", Comment: "v0_1_0", Checksum: "current-001"},
		"002": {Name: "002", Comment: "v0_2_0", Checksum: "current-002"},
	}

	t.Run("strict blocks boot and writes nothing", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		ctx := t.Context()

		db := newGuardTestDB(t, []string{"001", "002"}, map[string]string{"001": "stale-001"})
		guard := migrationguard.New(db, expected)

		mismatches, err := guard.Reconcile(ctx, migrationguard.ModeStrict)
		r.NoError(err, "a mismatch is reported as data, not a Reconcile error")
		r.Len(mismatches, 1)
		r.Equal("001", mismatches[0].Name)

		r.Error(migrationguard.ModeStrict.Apply(mismatches), "strict must turn the mismatch into a boot failure")

		_, ok := recordedChecksum(t, db, "002")
		r.False(ok, "strict mode must not backfill an unrelated row while any mismatch exists")

		sum, ok := recordedChecksum(t, db, "001")
		r.True(ok)
		r.Equal("stale-001", sum, "a mismatched row must never be silently overwritten")
	})

	// Positive control: identical fixture, warn mode.
	t.Run("warn boots and still backfills the unrelated row", func(t *testing.T) {
		t.Parallel()

		r := require.New(t)
		ctx := t.Context()

		db := newGuardTestDB(t, []string{"001", "002"}, map[string]string{"001": "stale-001"})
		guard := migrationguard.New(db, expected)

		mismatches, err := guard.Reconcile(ctx, migrationguard.ModeWarn)
		r.NoError(err)
		r.Len(mismatches, 1)
		r.Equal("001", mismatches[0].Name, "warn mode still surfaces the mismatch as data")

		r.NoError(migrationguard.ModeWarn.Apply(mismatches), "warn mode must never block boot")

		sum, ok := recordedChecksum(t, db, "002")
		r.True(ok, "warn mode must still backfill a row with no recorded checksum")
		r.Equal("current-002", sum)

		stale, ok := recordedChecksum(t, db, "001")
		r.True(ok)
		r.Equal("stale-001", stale, "warn mode must never overwrite a mismatched row's checksum either")
	})
}

// TestRepairFixesDriftAndIsNoOpOnCleanDB proves Repair is the explicit
// exception to "never overwrite a mismatched row": it fixes exactly the
// diverged/missing rows and reports what changed, then does nothing on a
// database it has already cleaned — and a strict Reconcile afterward is
// clean, proving repair actually un-blocks a boot that would otherwise fail.
func TestRepairFixesDriftAndIsNoOpOnCleanDB(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	expected := map[string]migrationguard.Migration{
		"001": {Name: "001", Comment: "v0_1_0", Checksum: "current-001"},
		"002": {Name: "002", Comment: "v0_2_0", Checksum: "current-002"},
	}

	db := newGuardTestDB(t, []string{"001", "002"}, map[string]string{"001": "stale-001"})
	guard := migrationguard.New(db, expected)

	results, err := guard.Repair(ctx)
	r.NoError(err)
	r.Len(results, 2, "one result for the drifted row, one for the missing row")

	byName := make(map[string]migrationguard.RepairResult, len(results))
	for _, res := range results {
		byName[res.Name] = res
	}

	r.Equal("stale-001", byName["001"].Old)
	r.Equal("current-001", byName["001"].New)
	r.Empty(byName["002"].Old, "a backfill has no prior checksum")
	r.Equal("current-002", byName["002"].New)

	for name, mig := range expected {
		sum, ok := recordedChecksum(t, db, name)
		r.True(ok)
		r.Equal(mig.Checksum, sum, "repair must leave the recorded checksum matching the current file")
	}

	mismatches, err := guard.Reconcile(ctx, migrationguard.ModeStrict)
	r.NoError(err)
	r.Empty(mismatches, "repair must leave the database clean for a strict boot")

	resultsAgain, err := guard.Repair(ctx)
	r.NoError(err)
	r.Empty(resultsAgain, "repair on an already-clean database must report nothing")
}
