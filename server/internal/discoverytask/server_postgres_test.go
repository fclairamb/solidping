package discoverytask

import (
	"testing"

	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portDiscoveryTaskPG is distinct from every other embedded-Postgres test port.
const portDiscoveryTaskPG = 15821

// TestKindContract_Postgres runs the discovery kind's result contract on a real
// Postgres (jsonb payloads, the discovered_checks upsert).
func TestKindContract_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	dbSvc, err := postgres.New(t.Context(), &postgres.Config{Embedded: true, Port: portDiscoveryTaskPG, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(t.Context()); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	runKindContract(t, dbSvc)
}
