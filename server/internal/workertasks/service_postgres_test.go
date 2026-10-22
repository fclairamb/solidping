package workertasks_test

import (
	"testing"

	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portWorkerTasksPG is distinct from every other embedded-Postgres test port
// (see db/postgres/postgres_headroom_postgres_test.go).
const portWorkerTasksPG = 15807

// TestService_Postgres runs the service suite on a real Postgres: the claim
// statement there takes FOR UPDATE SKIP LOCKED. Self-skips under -short.
func TestService_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	dbSvc, err := postgres.New(t.Context(), &postgres.Config{Embedded: true, Port: portWorkerTasksPG, RunMode: "test"})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(t.Context()); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	runServiceSuite(t, dbSvc)
}
