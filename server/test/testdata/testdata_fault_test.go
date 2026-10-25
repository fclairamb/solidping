package testdata_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
	"github.com/fclairamb/solidping/server/internal/testsupport"
	"github.com/fclairamb/solidping/server/test/testdata"
)

// TestCreateTestDataReportsEveryQueryFailure seeds the test-mode data once
// per query it issues, failing a different query each time: the seeder must
// report the failure instead of panicking or half-succeeding silently.
func TestCreateTestDataReportsEveryQueryFailure(t *testing.T) {
	t.Parallel()
	localfs.Register()

	cfg := &config.Config{FileStorage: config.FileStorageConfig{Type: "local", LocalRoot: t.TempDir()}}
	failures := 0
	runs := 0

	testsupport.SweepFresh(t, func() (*testsupport.FaultHook, func()) {
		ctx := t.Context()
		dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
		require.NoError(t, err)
		require.NoError(t, dbSvc.Initialize(ctx))
		hook := testsupport.InstallFaults(dbSvc.DB())

		return hook, func() {
			defer func() { _ = dbSvc.Close() }()
			runs++
			if testdata.CreateTestData(ctx, dbSvc, cfg) != nil {
				failures++
			}
			// Second call: the data exists (or the first failed), never a panic.
			_ = testdata.CreateTestData(ctx, dbSvc, cfg)
		}
	})

	require.Greater(t, runs, 10)
	require.Greater(t, failures, runs/2, "failed queries surfaced: %d of %d runs", failures, runs)
}
