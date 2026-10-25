package crawltask

import (
	"testing"

	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portCrawlTaskPG is distinct from every other embedded-Postgres test port.
const portCrawlTaskPG = 15813

// TestMaterialization_Postgres runs the materialization contract on a real
// Postgres: the due-job SELECT there takes FOR UPDATE SKIP LOCKED and the
// task insert ON CONFLICT DO NOTHING on the partial unique index.
func TestMaterialization_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	dbSvc, err := postgres.New(t.Context(), &postgres.Config{Embedded: true, Port: portCrawlTaskPG, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(t.Context()); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	runMaterialization(t, dbSvc)
}
