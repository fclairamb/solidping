package sqlite

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/migrationguard"
)

// releasedV0384Migration027 is 027 as v0.38.4 shipped it: the v0.38.1 file
// with what is now 028 appended.
const releasedV0384Migration027 = "testdata/027_v0_39_0.v0_38_4.up.sql"

// migrationsAsReleased returns the embedded migrations as a release shipped
// them: without 028, and for v0.38.4 with its edited 027.
func migrationsAsReleased(t *testing.T, release string) fstest.MapFS {
	t.Helper()

	out := fstest.MapFS{}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	require.NoError(t, err)

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "028_") {
			continue
		}

		body, readErr := fs.ReadFile(migrationsFS, "migrations/"+entry.Name())
		require.NoError(t, readErr)

		out["migrations/"+entry.Name()] = &fstest.MapFile{Data: body}
	}

	if release == "v0.38.4" {
		body, readErr := os.ReadFile(releasedV0384Migration027)
		require.NoError(t, readErr)

		out["migrations/027_v0_39_0.up.sql"] = &fstest.MapFile{Data: body}
	}

	return out
}

func tableHasColumn(t *testing.T, svc *Service, table, column string) bool {
	t.Helper()

	var count int

	require.NoError(t, svc.db.NewRaw(
		"SELECT count(*) FROM pragma_table_info(?) WHERE name = ?", table, column,
	).Scan(t.Context(), &count))

	return count == 1
}

// TestSupersededChecksumMatchesTheV0384Release pins the guard entry to the
// file v0.38.4 really shipped.
func TestSupersededChecksumMatchesTheV0384Release(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile(releasedV0384Migration027)
	require.NoError(t, err)

	sum := sha256.Sum256(body)
	require.Equal(t, hex.EncodeToString(sum[:]), supersededMigrations()[0].Checksum)
}

// TestUpgradeFromEitherCopyOfMigration027 boots this binary on a database
// built by v0.38.3 (027 as released in v0.38.1) and by v0.38.4 (027 with the
// 028 sections appended). v0.38.4 itself crashed on the first one. Both must
// boot, end with the 028 columns, and record 027 and 028 at their current
// checksums.
func TestUpgradeFromEitherCopyOfMigration027(t *testing.T) {
	t.Parallel()

	columns := [][2]string{
		{"check_jobs", "run_requested_at"},
		{"checks", "reminder_interval_seconds"},
		{"incidents", "reminder_count"},
		{"incidents", "last_reminder_at"},
	}

	for _, tc := range []struct {
		release     string
		hasColumns  bool
		recorded027 string
	}{
		{release: "v0.38.3", hasColumns: false},
		{release: "v0.38.4", hasColumns: true, recorded027: supersededMigrations()[0].Checksum},
	} {
		t.Run(tc.release, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			r := require.New(t)
			dir := t.TempDir()

			old, err := newService(ctx, &Config{DataDir: dir}, migrationsAsReleased(t, tc.release))
			r.NoError(err)
			r.NoError(old.Initialize(ctx))

			for _, c := range columns {
				r.Equal(tc.hasColumns, tableHasColumn(t, old, c[0], c[1]), "%s before the upgrade: %s.%s", tc.release, c[0], c[1])
			}

			if tc.recorded027 != "" {
				var sum string
				r.NoError(old.db.NewRaw("SELECT checksum FROM migration_checksums WHERE name = '027'").Scan(ctx, &sum))
				r.Equal(tc.recorded027, sum)
			}

			if tc.recorded027 != "" {
				// Negative control: without the superseded entry this is the
				// mismatch that crashed v0.38.4.
				want, sumErr := migrationguard.Checksums(migrationsFS, "migrations")
				r.NoError(sumErr)

				mismatches, recErr := migrationguard.New(old.db, want).Reconcile(ctx, migrationguard.ModeStrict)
				r.NoError(recErr)
				r.Len(mismatches, 1)
				r.Equal("027", mismatches[0].Name)
			}

			r.NoError(old.Close())

			svc, err := New(ctx, Config{DataDir: dir})
			r.NoError(err)
			t.Cleanup(func() { _ = svc.Close() })

			r.NoError(svc.Initialize(ctx), "upgrading from %s must boot", tc.release)

			for _, c := range columns {
				r.True(tableHasColumn(t, svc, c[0], c[1]), "%s.%s after the upgrade", c[0], c[1])
			}

			want, err := migrationguard.Checksums(migrationsFS, "migrations")
			r.NoError(err)

			for _, name := range []string{"027", "028"} {
				var sum string
				r.NoError(svc.db.NewRaw("SELECT checksum FROM migration_checksums WHERE name = ?", name).Scan(ctx, &sum))
				r.Equal(want[name].Checksum, sum, "%s is recorded at its current checksum", name)
			}

			var applied028 int
			r.NoError(svc.db.NewRaw("SELECT count(*) FROM bun_migrations WHERE name = '028'").Scan(ctx, &applied028))
			r.Equal(1, applied028, "028 is recorded once")

			// A second boot is clean: nothing left to resolve or run.
			r.NoError(svc.Initialize(ctx))
		})
	}
}
