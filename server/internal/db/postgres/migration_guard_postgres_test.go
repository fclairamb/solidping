package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/migrationguard"
)

// portMigrationGuard is distinct from every other _postgres_test.go file's
// embedded-Postgres port in this repo (see the port-numbering note in
// postgres_headroom_postgres_test.go).
const portMigrationGuard = 15477

// tamperedMigrationName is a migration id shipped in both dialects'
// migrations directories, used to model an already-applied migration whose
// file content was rewritten after it ran.
const tamperedMigrationName = "013"

func recordedChecksumPG(t *testing.T, svc *Service, name string) string {
	t.Helper()

	var sum string

	require.NoError(t, svc.db.NewRaw(
		"SELECT checksum FROM migration_checksums WHERE name = ?", name,
	).Scan(t.Context(), &sum))

	return sum
}

// TestMigrationGuardModePairPG mirrors the sqlite package's
// TestMigrationGuardFailsBootOnRewrittenMigration /
// TestMigrationGuardWarnModeBootsOnRewrittenMigration pair, proving the two
// engines stay behaviorally identical: the same tampered checksum row fails a
// strict boot and succeeds (without repairing itself) a warn boot.
func TestMigrationGuardModePairPG(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portMigrationGuard, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	r.NoError(svc.Initialize(ctx), "the first boot applies and records every migration")

	original := recordedChecksumPG(t, svc, tamperedMigrationName)
	r.NotEmpty(original)

	_, err = svc.db.ExecContext(ctx,
		"UPDATE migration_checksums SET checksum = 'deadbeef' WHERE name = ?", tamperedMigrationName)
	r.NoError(err)

	err = svc.Initialize(ctx)
	r.Error(err, "strict mode (the default) must fail the boot on a checksum mismatch")
	r.ErrorIs(err, migrationguard.ErrChecksumMismatch)
	r.Equal("deadbeef", recordedChecksumPG(t, svc, tamperedMigrationName),
		"a failing boot must not overwrite the recorded checksum")

	// Positive control: the identical tampered row, warn mode — must boot.
	svc.guardMode = migrationguard.ModeWarn
	r.NoError(svc.Initialize(ctx), "warn mode must let the same mismatch boot")
	r.Equal("deadbeef", recordedChecksumPG(t, svc, tamperedMigrationName),
		"warn mode must not overwrite a mismatched row's checksum either")
}

// TestRepairMigrationChecksumsPG proves RepairMigrationChecksums is wired for
// the Postgres engine too: no-op on a clean database, fixes a diverged row,
// and leaves the database clean for a subsequent strict boot.
func TestRepairMigrationChecksumsPG(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: portMigrationGuard + 1, RunMode: runModeTest})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	r.NoError(svc.Initialize(ctx))

	results, err := svc.RepairMigrationChecksums(ctx)
	r.NoError(err)
	r.Empty(results, "a healthy database has nothing to repair")

	original := recordedChecksumPG(t, svc, tamperedMigrationName)
	_, err = svc.db.ExecContext(ctx,
		"UPDATE migration_checksums SET checksum = 'deadbeef' WHERE name = ?", tamperedMigrationName)
	r.NoError(err)

	results, err = svc.RepairMigrationChecksums(ctx)
	r.NoError(err)
	r.Len(results, 1)
	r.Equal(tamperedMigrationName, results[0].Name)
	r.Equal("deadbeef", results[0].Old)
	r.Equal(original, results[0].New)

	r.NoError(svc.Initialize(ctx), "a repaired database boots cleanly in strict mode")

	resultsAgain, err := svc.RepairMigrationChecksums(ctx)
	r.NoError(err)
	r.Empty(resultsAgain, "repairing an already-clean database must report nothing")
}
