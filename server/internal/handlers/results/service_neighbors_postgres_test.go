package results

import (
	"testing"

	"github.com/fclairamb/solidping/server/internal/db/postgres"
)

// TestGetResultNeighbors_Postgres exercises the same
// testGetResultNeighborsAcrossBackend assertions (service_neighbors_test.go)
// against a real embedded PostgreSQL, proving the SQLite-tested
// previousUid/nextUid computation (spec 2026-07-05-14) holds identically on
// both backends — GetResultNeighbors is implemented with plain bun queries
// with no dialect-specific SQL in either postgres.go or sqlite.go.
//
// Self-skips under `-short` (the default `make test` mode) and on any
// embedded-startup error, mirroring
// internal/checkworker/checkjobsvc/service_postgres_test.go. Uses port 15445
// — distinct from every other embedded-postgres port claimed in the repo
// (5435, 15434, 15436, 15437, 15438) — to avoid colliding if the full
// (non -short) suite ever runs them concurrently.
func TestGetResultNeighbors_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded:    true,
		EmbeddedDir: t.TempDir(),
		Port:        15445,
		RunMode:     "test",
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	testGetResultNeighborsAcrossBackend(ctx, t, dbSvc)
}
