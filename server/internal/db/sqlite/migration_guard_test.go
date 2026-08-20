package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/migrationguard"
)

func recordedChecksum(ctx context.Context, t *testing.T, svc *Service) string {
	t.Helper()

	var sum string

	require.NoError(t, svc.db.NewRaw(
		"SELECT checksum FROM migration_checksums WHERE name = ?", "013",
	).Scan(ctx, &sum))

	return sum
}

// TestMigrationGuardFailsBootOnRewrittenMigration is the regression test spec
// 2026-08-18-02 asks for: a migration whose recorded content no longer matches
// the content this binary carries must FAIL the boot, naming the migration,
// instead of being silently skipped the way 013 was.
func TestMigrationGuardFailsBootOnRewrittenMigration(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })

	r.NoError(svc.Initialize(ctx), "the first boot applies and records every migration")

	// Positive control FIRST: an untouched database boots again cleanly, so a
	// failure below can only come from the tampering.
	r.NoError(svc.Initialize(ctx), "an unchanged database must keep booting")

	original := recordedChecksum(ctx, t, svc)
	r.NotEmpty(original, "013's checksum must have been recorded")

	// Rewrite 013's recorded content — the database's record of what it ran no
	// longer matches the file, which is exactly what happens when an applied
	// migration is edited in place.
	_, err = svc.db.ExecContext(ctx,
		"UPDATE migration_checksums SET checksum = 'deadbeef' WHERE name = '013'")
	r.NoError(err)

	err = svc.Initialize(ctx)
	r.Error(err, "boot must fail once an applied migration's content has changed")
	r.ErrorIs(err, migrationguard.ErrChecksumMismatch)

	var mismatch *migrationguard.MismatchError
	r.ErrorAs(err, &mismatch)
	r.Equal("013", mismatch.Name)
	r.Equal("v0_16_0", mismatch.Comment)
	r.Equal("deadbeef", mismatch.Recorded)
	r.Equal(original, mismatch.Current)

	// The message has to be actionable, not merely correct.
	r.Contains(err.Error(), "013_v0_16_0", "the error must name the migration")
	r.Contains(err.Error(), "reset the database")
	r.Contains(err.Error(), migrationguard.ChecksumTable)

	// The guard must NOT quietly repair its own record: a diverged database
	// stays failing until a human decides how to fix it.
	r.Equal("deadbeef", recordedChecksum(ctx, t, svc),
		"a failing boot must not overwrite the recorded checksum")
}

// TestMigrationGuardBackfillsExistingDatabases proves the guard does not turn
// every already-deployed database into a startup failure on the day it ships:
// a database whose migrations were applied before the guard existed has no
// checksum rows at all, and the first boot must record them rather than reject
// them.
func TestMigrationGuardBackfillsExistingDatabases(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	// Model a pre-guard database: migrations applied, nothing recorded.
	_, err = svc.db.ExecContext(ctx, "DELETE FROM migration_checksums")
	r.NoError(err)

	var before int
	r.NoError(svc.db.NewRaw("SELECT count(*) FROM migration_checksums").Scan(ctx, &before))
	r.Zero(before)

	r.NoError(svc.Initialize(ctx), "a pre-guard database must boot and be backfilled")

	var after int
	r.NoError(svc.db.NewRaw("SELECT count(*) FROM migration_checksums").Scan(ctx, &after))
	r.Positive(after, "the guard must backfill checksums for already-applied migrations")

	var applied int
	r.NoError(svc.db.NewRaw("SELECT count(*) FROM bun_migrations").Scan(ctx, &applied))
	r.Equal(applied, after, "every applied migration must get a recorded checksum")

	// And the backfilled values are the real ones, so the guard is armed from
	// here on rather than merely quiet.
	_, err = svc.db.ExecContext(ctx,
		"UPDATE migration_checksums SET checksum = 'tampered' WHERE name = '001'")
	r.NoError(err)
	r.ErrorIs(svc.Initialize(ctx), migrationguard.ErrChecksumMismatch)
}

// TestMigrationGuardIgnoresUnknownMigrations proves a database carrying a
// migration this binary no longer ships (a rolled-back release, or a downgrade)
// is not treated as drift. bun already tolerates that; the guard must not be
// stricter than the thing it guards.
func TestMigrationGuardIgnoresUnknownMigrations(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	_, err = svc.db.ExecContext(ctx,
		"INSERT INTO bun_migrations (name, group_id) VALUES ('999', 1)")
	r.NoError(err)

	r.NoError(svc.Initialize(ctx), "a migration from a newer build must not fail the boot")
}

// TestMigrationGuardWarnModeBootsOnRewrittenMigration is the positive control
// for TestMigrationGuardFailsBootOnRewrittenMigration above: the identical
// tampered database, but configured with GuardMode: warn, must boot instead
// of failing — and must not quietly repair the stale record either, which is
// what makes the warning recur on every boot until an operator runs
// `migrate repair`.
func TestMigrationGuardWarnModeBootsOnRewrittenMigration(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true, GuardMode: migrationguard.ModeWarn})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })

	r.NoError(svc.Initialize(ctx), "the first boot applies and records every migration")

	original := recordedChecksum(ctx, t, svc)
	r.NotEmpty(original)

	_, err = svc.db.ExecContext(ctx,
		"UPDATE migration_checksums SET checksum = 'deadbeef' WHERE name = '013'")
	r.NoError(err)

	// Same tampering the strict test above proves fails the boot — here it
	// must not.
	r.NoError(svc.Initialize(ctx), "warn mode must let a boot with a checksum mismatch continue")

	r.Equal("deadbeef", recordedChecksum(ctx, t, svc),
		"warn mode must not overwrite a mismatched row's checksum — that would silence the warning")
}

// TestRepairMigrationChecksums proves the explicit repair path: it fixes a
// diverged record, is a no-op on a healthy database, and leaves the database
// clean enough for a subsequent strict boot to succeed — the exact scenario
// the previous test's tampering would otherwise block forever.
func TestRepairMigrationChecksums(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	results, err := svc.RepairMigrationChecksums(ctx)
	r.NoError(err)
	r.Empty(results, "a healthy database has nothing to repair")

	original := recordedChecksum(ctx, t, svc)
	r.NotEmpty(original)

	_, err = svc.db.ExecContext(ctx,
		"UPDATE migration_checksums SET checksum = 'deadbeef' WHERE name = '013'")
	r.NoError(err)

	results, err = svc.RepairMigrationChecksums(ctx)
	r.NoError(err)
	r.Len(results, 1)
	r.Equal("013", results[0].Name)
	r.Equal("deadbeef", results[0].Old)
	r.Equal(original, results[0].New)

	r.Equal(original, recordedChecksum(ctx, t, svc),
		"repair must re-record the current file's checksum")

	// Repair must not have run any migration — the database boots clean in
	// strict mode again, proving repair alone was the fix.
	r.NoError(svc.Initialize(ctx), "a repaired database boots cleanly in strict mode")

	resultsAgain, err := svc.RepairMigrationChecksums(ctx)
	r.NoError(err)
	r.Empty(resultsAgain, "repairing an already-clean database must report nothing")
}
