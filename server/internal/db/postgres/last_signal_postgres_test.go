package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// portLastSignal is distinct from every other _postgres_test.go file's
// embedded-Postgres port in this repo (see the port-numbering note in
// last_result_test.go).
const portLastSignal = 15510

// GetLastSignalForChecks (spec 2026-09-02-03) is GetLastResultForChecks
// narrowed to INBOUND SIGNAL rows — the ones handlers/heartbeat and
// handlers/emailcheck write, which never carry a worker_uid — so the passive
// evaluator stops reading its own previous output. These are the Postgres
// twins of internal/db/sqlite/last_signal_test.go.

// newSignalPG boots an embedded Postgres for one signal test, self-skipping
// like every other embedded-postgres test in this package.
func newSignalPG(t *testing.T, port uint32) (*Service, context.Context) {
	t.Helper()

	ctx := t.Context()

	s, err := New(ctx, &Config{
		Embedded: true,
		Port:     port,
		RunMode:  runModeTest,
	})
	if err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}

	t.Cleanup(func() { _ = s.Close() })

	if initErr := s.Initialize(ctx); initErr != nil {
		t.Skipf("embedded postgres init failed: %v", initErr)
	}

	return s, ctx
}

// seedSignalRowPG inserts one raw row. workerUID nil makes it a SIGNAL row
// (what the heartbeat/email ingest writes); non-nil makes it an EVALUATION
// row (what checkworker/backend.DirectBackend.SubmitResult writes).
func seedSignalRowPG(
	t *testing.T, s *Service, ctx context.Context, //nolint:revive // ctx after t matches this package's helpers
	orgUID, checkUID string, status models.ResultStatus, at time.Time, workerUID *string,
) *models.Result {
	t.Helper()

	row := models.NewResult(orgUID, checkUID, status, 0)
	row.PeriodStart = at
	row.WorkerUID = workerUID
	require.NoError(t, s.CreateResult(ctx, row))

	return row
}

// newSignalWorkerPG inserts a real worker row: results.worker_uid is a
// foreign key, so an evaluation row cannot be seeded with an invented uid.
func newSignalWorkerPG(
	//nolint:revive // ctx after t matches this package's helpers
	t *testing.T, s *Service, ctx context.Context, identifier string,
) string {
	t.Helper()

	worker := models.NewWorker(identifier, identifier)
	_, err := s.db.NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)

	return worker.UID
}

// TestGetLastSignalForChecks_Postgres pins the row-level contract, one
// subtest per rule, against a single embedded instance (booting one per rule
// would multiply the slowest part of the test by five).
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with its siblings
func TestGetLastSignalForChecks_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	s, ctx := newSignalPG(t, portLastSignal)

	org := models.NewOrganization("signal-pg-org", "Signal PG Org")
	require.NoError(t, s.CreateOrganization(ctx, org))

	worker := newSignalWorkerPG(t, s, ctx, "signal-pg-w")
	base := time.Now().Add(time.Hour)

	t.Run("SkipsWorkerRows", func(t *testing.T) {
		r := require.New(t)

		check := models.NewCheck(org.UID, "beating", "heartbeat")
		r.NoError(s.CreateCheck(ctx, check))

		beat := seedSignalRowPG(t, s, ctx, org.UID, check.UID, models.ResultStatusUp, base, nil)

		var newestEvaluation *models.Result
		for i := 1; i <= 3; i++ {
			newestEvaluation = seedSignalRowPG(t, s, ctx, org.UID, check.UID,
				models.ResultStatusUp, base.Add(time.Duration(i)*time.Minute), &worker)
		}

		signals, err := s.GetLastSignalForChecks(ctx, org.UID, []string{check.UID})
		r.NoError(err)
		r.Len(signals, 1)
		r.Equal(beat.UID, signals[check.UID].UID,
			"the signal lookup must descend past every worker-written row to the beat")

		// The two queries must never be collapsed: lastResult still means
		// "the newest row of any origin", which is what the API surfaces.
		results, err := s.GetLastResultForChecks(ctx, org.UID, []string{check.UID})
		r.NoError(err)
		r.Equal(newestEvaluation.UID, results[check.UID].UID)
	})

	t.Run("SkipsCreatedMarker", func(t *testing.T) {
		r := require.New(t)

		check := models.NewCheck(org.UID, "never-beaten", "heartbeat")
		r.NoError(s.CreateCheck(ctx, check))

		marker := new(models.Result)
		r.NoError(s.db.NewSelect().Model(marker).Where("check_uid = ?", check.UID).Scan(ctx))
		r.NotNil(marker.Status)
		r.Equal(int(models.ResultStatusCreated), *marker.Status)
		r.Nil(marker.WorkerUID, "precondition: the creation marker carries no worker_uid either")

		signals, err := s.GetLastSignalForChecks(ctx, org.UID, []string{check.UID})
		r.NoError(err)
		r.NotContains(signals, check.UID, "a check created but never beaten has no signal")

		beat := seedSignalRowPG(t, s, ctx, org.UID, check.UID, models.ResultStatusUp, base, nil)

		signals, err = s.GetLastSignalForChecks(ctx, org.UID, []string{check.UID})
		r.NoError(err)
		r.Equal(beat.UID, signals[check.UID].UID)
	})

	t.Run("EvaluationOnlyCheckIsAbsent", func(t *testing.T) {
		r := require.New(t)

		check := models.NewCheck(org.UID, "silent", "heartbeat")
		r.NoError(s.CreateCheck(ctx, check))

		for i := 0; i < 5; i++ {
			seedSignalRowPG(t, s, ctx, org.UID, check.UID,
				models.ResultStatusDown, base.Add(time.Duration(i)*time.Minute), &worker)
		}

		// A reaper row (abandoned) is worker-written too and falls out via the
		// same predicate, with no status rule of its own.
		seedSignalRowPG(t, s, ctx, org.UID, check.UID,
			models.ResultStatusAbandoned, base.Add(6*time.Minute), &worker)

		signals, err := s.GetLastSignalForChecks(ctx, org.UID, []string{check.UID})
		r.NoError(err)
		r.NotContains(signals, check.UID, "a check with only worker-written rows has no signal")
	})

	t.Run("OneEntryPerRequestedCheckAndOrgScoped", func(t *testing.T) {
		r := require.New(t)

		otherOrg := models.NewOrganization("signal-pg-org-b", "Signal PG Org B")
		r.NoError(s.CreateOrganization(ctx, otherOrg))

		checkA := models.NewCheck(org.UID, "beat-a", "heartbeat")
		checkB := models.NewCheck(otherOrg.UID, "beat-b", "heartbeat")
		r.NoError(s.CreateCheck(ctx, checkA))
		r.NoError(s.CreateCheck(ctx, checkB))

		seedSignalRowPG(t, s, ctx, org.UID, checkA.UID, models.ResultStatusUp, base, nil)
		newestA := seedSignalRowPG(t, s, ctx, org.UID, checkA.UID,
			models.ResultStatusUp, base.Add(time.Minute), nil)
		seedSignalRowPG(t, s, ctx, otherOrg.UID, checkB.UID, models.ResultStatusUp, base, nil)

		signals, err := s.GetLastSignalForChecks(ctx, org.UID, []string{
			checkA.UID, checkA.UID, checkB.UID, models.NewCheck(org.UID, "x", "http").UID,
		})
		r.NoError(err)
		r.Len(signals, 1, "one entry per requested check that has a signal in THIS org")
		r.Equal(newestA.UID, signals[checkA.UID].UID)
		r.Equal(models.PeriodTypeRaw, signals[checkA.UID].PeriodType)
		r.NotContains(signals, checkB.UID, "another organization's check must not leak")

		empty, err := s.GetLastSignalForChecks(ctx, org.UID, nil)
		r.NoError(err)
		r.NotNil(empty)
		r.Empty(empty)
	})
}
