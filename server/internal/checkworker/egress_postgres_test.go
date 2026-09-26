package checkworker

import (
	"testing"

	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portEgressPG is distinct from every other _postgres_test.go embedded-Postgres
// port claimed in the repo (see the port-numbering comment in
// db/postgres/postgres_headroom_postgres_test.go).
const portEgressPG = 15546

// TestExecuteJob_EgressPolicy_Postgres is the Postgres twin of
// TestExecuteJob_EgressPolicy_SQLite: a real `http` check executed through
// executeJob is refused (Error, policy message, egress_denied) by a denying
// worker and succeeds under an allowing one, with the result persisted on a
// REAL Postgres backend. Self-skips under -short.
//
//nolint:paralleltest // shares the process-level browser/js activation globals newCheckWorker installs
func TestExecuteJob_EgressPolicy_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{Embedded: true, Port: portEgressPG, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	assertEgressDeniedThenAllowed(t, ctx, dbSvc)
}
