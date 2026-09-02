package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun/dialect/pgdialect"

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
	t *testing.T, s *Service, ctx context.Context, identifier string, //nolint:revive // ctx after t matches this package's helpers
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

// TestGetLastSignalForChecks_UsesTheSignalIndex_Postgres asserts the plan
// rides results_raw_signal_idx (migration 018) rather than results_raw_idx or
// a sequential scan.
//
// This is why the partial index exists: results_raw_idx does not carry
// worker_uid, so on a check that has stopped beating the descent walks every
// evaluation row inside raw retention before it reaches the beat — 1440 heap
// fetches per tick at a 1-minute period with the 24 h default, every minute,
// for every silent check. The fixture puts 500 evaluation rows on top of one
// old beat precisely so the wrong plan would be measurably wrong.
//
//nolint:paralleltest // shares dev-machine resources (embedded-postgres-go's pwfile extraction) with its siblings
func TestGetLastSignalForChecks_UsesTheSignalIndex_Postgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping embedded-postgres test in -short mode")
	}

	r := require.New(t)
	s, ctx := newSignalPG(t, portLastSignal+2)

	org := models.NewOrganization("signal-plan-org", "Signal Plan Org")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "planned", "heartbeat")
	r.NoError(s.CreateCheck(ctx, check))

	worker := newSignalWorkerPG(t, s, ctx, "signal-plan-w")
	base := time.Now().Add(time.Hour)

	// One old beat, then a wall of evaluation rows on top of it — the shape a
	// heartbeat that stopped beating has.
	seedSignalRowPG(t, s, ctx, org.UID, check.UID, models.ResultStatusUp, base, nil)

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO results (uid, organization_uid, check_uid, period_type, period_start, worker_uid, status, duration)
		 SELECT gen_random_uuid(), ?, ?, 'raw', ?::timestamptz + (i * interval '1 minute'), ?, ?, 0
		 FROM generate_series(1, 500) AS i`,
		org.UID, check.UID, base, worker, int(models.ResultStatusDown))
	r.NoError(err)

	// Noise from other checks, so the target's rows are not the whole table
	// and an index scan is the planner's genuine choice.
	for i := range 5 {
		noise := models.NewCheck(org.UID, "plan-noise-"+string(rune('a'+i)), "http")
		r.NoError(s.CreateCheck(ctx, noise))
		_, noiseErr := s.db.ExecContext(ctx,
			`INSERT INTO results (uid, organization_uid, check_uid, period_type, period_start, worker_uid, status, duration)
			 SELECT gen_random_uuid(), ?, ?, 'raw', ?::timestamptz + (i * interval '1 minute'), ?, ?, 0
			 FROM generate_series(1, 500) AS i`,
			org.UID, noise.UID, base, worker, int(models.ResultStatusUp))
		r.NoError(noiseErr)
	}

	_, err = s.db.ExecContext(ctx, "ANALYZE results")
	r.NoError(err)

	// Render the exact statement production runs (same package constant, same
	// argument order) with its arguments inlined, so EXPLAIN can plan it.
	raw := s.db.NewRaw(lastSignalForChecksQuery,
		pgdialect.Array([]string{check.UID}), org.UID, int(models.ResultStatusCreated))
	rendered, err := raw.AppendQuery(s.db.QueryGen(), nil)
	r.NoError(err)

	rows, err := s.db.QueryContext(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+string(rendered))
	r.NoError(err)

	defer func() { _ = rows.Close() }()

	var plan strings.Builder

	for rows.Next() {
		var line string

		r.NoError(rows.Scan(&line))
		plan.WriteString(line)
		plan.WriteString("\n")
	}

	r.NoError(rows.Err())

	steps := plan.String()

	r.Contains(steps, "results_raw_signal_idx",
		"the signal lookup must ride the dedicated partial index:\n%s", steps)
	r.NotContains(steps, "Seq Scan on results",
		"the signal lookup must never scan the results table:\n%s", steps)
}
