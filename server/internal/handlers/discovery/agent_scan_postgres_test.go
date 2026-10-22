package discovery_test

import (
	"testing"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/egress"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portDiscoveryPG is distinct from every other embedded-Postgres test port.
const portDiscoveryPG = 15831

// TestAgentScanConcurrentStarts_Postgres runs the concurrent-start test on a
// real Postgres, where the per-org advisory transaction lock serializes the
// starts across pool connections. Self-skips under -short.
func TestAgentScanConcurrentStarts_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	dbSvc, err := postgres.New(t.Context(), &postgres.Config{Embedded: true, Port: portDiscoveryPG, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(t.Context()); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	f := newDiscoveryFixtureOn(t, dbSvc, egress.New(false), true)
	runConcurrentStarts(t, newAgentScanFixtureFrom(t, f, config.DeploymentModeSaaS))
}
