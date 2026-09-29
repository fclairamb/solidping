package auth

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portImpersonatePG is distinct from every other embedded-Postgres port
// claimed in the repo (see the port-numbering note in
// internal/db/incident_number_test.go).
const portImpersonatePG = 15625

// TestImpersonate_Postgres is the real-engine twin of TestImpersonate: the
// same contract, on one embedded Postgres, each sub-test on its own orgs and
// users (and its own Service, since the kill-switch case mutates config).
func TestImpersonate_Postgres(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbService, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portImpersonatePG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbService.Close() })

	if initErr := dbService.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	var seq atomic.Int64

	runImpersonationContract(t, func(t *testing.T) (*impersonationFixture, context.Context) {
		t.Helper()

		cfg := impersonationTestConfig()
		svc := NewService(dbService, cfg.Auth, cfg, nil, nil)
		subCtx := t.Context()

		return newImpersonationFixture(subCtx, t, svc, dbService, "p"+strconv.FormatInt(seq.Add(1), 10)), subCtx
	})
}
