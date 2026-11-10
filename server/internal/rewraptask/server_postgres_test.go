package rewraptask_test

import (
	"testing"

	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portRewrapTaskPG is distinct from every other embedded-Postgres test port.
const portRewrapTaskPG = 15845

// TestRewrap_Postgres runs the main re-wrap path on a real Postgres: the
// targeted claim, the jsonb payload and the compare-and-set swap.
func TestRewrap_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	dbSvc, err := postgres.New(t.Context(), &postgres.Config{Embedded: true, Port: portRewrapTaskPG, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(t.Context()); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	runNewAgentGetsTheBlob(t, newHarnessOn(t, dbSvc, "acme-pg"))
}
