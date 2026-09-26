package incidents_test

import (
	"testing"

	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// portIncidentsQuorumPG is distinct from every other embedded-Postgres port
// claimed in the repo.
const portIncidentsQuorumPG = 15544

// TestQuorum_Postgres runs every multi-region quorum scenario (spec
// 2026-09-25-10) against real Postgres: the per-region upsert (ON CONFLICT
// with the status_since CASE and the ordering guard), the live-row read of
// regions/fail_quorum and the state machine on top of them. Same scenario
// code as the SQLite tests in quorum_test.go, one org per scenario.
//
//nolint:paralleltest // subtests share one embedded Postgres instance
func TestQuorum_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	ctx := t.Context()

	dbSvc, err := postgres.New(ctx, &postgres.Config{
		Embedded: true,
		Port:     portIncidentsQuorumPG,
		RunMode:  "test",
	})
	if err != nil {
		testsupport.PostgresUnavailable(t, err)
	}

	t.Cleanup(func() { _ = dbSvc.Close() })

	if initErr := dbSvc.Initialize(ctx); initErr != nil {
		testsupport.PostgresInitFailed(t, initErr)
	}

	scenarios := []struct {
		org string
		run func(*testing.T, *quorumWorld)
	}{
		{org: "quorum-regional", run: scenarioOneRegionFailingIsRegional},
		{org: "quorum-recovery", run: scenarioRecoveryMirrorsQuorum},
		{org: "quorum-integer", run: scenarioExplicitIntegerQuorum},
		{org: "quorum-replaced", run: scenarioReplacedRegionDoesNotCount},
		{org: "quorum-stale", run: scenarioStaleRegionReadingIgnored},
		{org: "quorum-legacy", run: scenarioLegacyEquivalence},
		{org: "quorum-since", run: scenarioRegionStateStatusSince},
	}

	for _, sc := range scenarios {
		t.Run(sc.org, func(t *testing.T) {
			sc.run(t, newQuorumWorld(t, dbSvc, sc.org))
		})
	}
}
