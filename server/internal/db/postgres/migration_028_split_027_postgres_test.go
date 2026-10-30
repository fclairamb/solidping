package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/migrationguard"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// TestSupersededChecksumMatchesTheV0384Release pins the guard entry to the
// file v0.38.4 really shipped (027 with what is now 028 appended).
func TestSupersededChecksumMatchesTheV0384Release(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("testdata/027_v0_39_0.v0_38_4.up.sql")
	require.NoError(t, err)

	sum := sha256.Sum256(body)
	require.Equal(t, hex.EncodeToString(sum[:]), supersededMigrations()[0].Checksum)
}

// TestUpgradeFromEitherCopyOfMigration027PG mirrors the sqlite test of the
// same name. It rewinds a migrated database to what v0.38.3 and v0.38.4 left
// behind, then boots it again.
func TestUpgradeFromEitherCopyOfMigration027PG(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	r := require.New(t)

	svc, err := New(ctx, &Config{Embedded: true, Port: testsupport.FreePort(t), RunMode: runModeTest})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	r.NoError(svc.Initialize(ctx))

	want, err := migrationguard.Checksums(svc.migrationFS(), "migrations")
	r.NoError(err)

	columns := [][2]string{
		{"check_jobs", "run_requested_at"},
		{"checks", "reminder_interval_seconds"},
		{"incidents", "reminder_count"},
		{"incidents", "last_reminder_at"},
	}

	hasColumn := func(table, column string) bool {
		var count int
		r.NoError(svc.db.NewRaw(
			"SELECT count(*) FROM information_schema.columns WHERE table_name = ? AND column_name = ?",
			table, column,
		).Scan(ctx, &count))

		return count == 1
	}

	forget028 := func() {
		_, execErr := svc.db.ExecContext(ctx, "DELETE FROM bun_migrations WHERE name = '028'")
		r.NoError(execErr)
		_, execErr = svc.db.ExecContext(ctx, "DELETE FROM migration_checksums WHERE name = '028'")
		r.NoError(execErr)
	}

	assertUpgraded := func(release string) {
		r.NoError(svc.Initialize(ctx), "upgrading from %s must boot", release)

		for _, c := range columns {
			r.True(hasColumn(c[0], c[1]), "%s: %s.%s after the upgrade", release, c[0], c[1])
		}

		for _, name := range []string{"027", "028"} {
			var sum string
			r.NoError(svc.db.NewRaw("SELECT checksum FROM migration_checksums WHERE name = ?", name).Scan(ctx, &sum))
			r.Equal(want[name].Checksum, sum, "%s: %s is recorded at its current checksum", release, name)
		}

		var applied int
		r.NoError(svc.db.NewRaw("SELECT count(*) FROM bun_migrations WHERE name = '028'").Scan(ctx, &applied))
		r.Equal(1, applied, "%s: 028 is recorded once", release)
	}

	// v0.38.3: 027 as released in v0.38.1, none of the 028 columns.
	forget028()

	for _, c := range columns {
		_, err = svc.db.ExecContext(ctx, "ALTER TABLE "+c[0]+" DROP COLUMN "+c[1])
		r.NoError(err)
	}

	assertUpgraded("v0.38.3")

	// v0.38.4: the columns exist, 027 is recorded at the edited checksum.
	forget028()

	_, err = svc.db.ExecContext(ctx,
		"UPDATE migration_checksums SET checksum = ? WHERE name = '027'", supersededMigrations()[0].Checksum)
	r.NoError(err)

	assertUpgraded("v0.38.4")
}
