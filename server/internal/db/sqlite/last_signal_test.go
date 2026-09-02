package sqlite

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// GetLastSignalForChecks (spec 2026-09-02-03) is GetLastResultForChecks
// narrowed to INBOUND SIGNAL rows — the ones handlers/heartbeat and
// handlers/emailcheck write, which never carry a worker_uid — so the passive
// evaluator stops reading its own previous output. These tests pin that
// narrowing; their Postgres twins live in last_signal_postgres_test.go.

// seedSignalRow inserts one raw row for checkUID. workerUID nil makes it a
// signal row; non-nil makes it an evaluation row (what
// checkworker/backend.DirectBackend.SubmitResult writes).
func seedSignalRow(
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

// newSignalWorker inserts a real worker row: results.worker_uid is a foreign
// key, so an evaluation row cannot be seeded with an invented uid.
func newSignalWorker(
	t *testing.T, s *Service, ctx context.Context, identifier string, //nolint:revive // ctx after t matches this package's helpers
) string {
	t.Helper()

	worker := models.NewWorker(identifier, identifier)
	_, err := s.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)

	return worker.UID
}

func newSignalTestService(t *testing.T) (*Service, context.Context) {
	t.Helper()

	ctx := t.Context()

	s, err := New(ctx, Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, s.Initialize(ctx))
	t.Cleanup(func() { _ = s.Close() })

	return s, ctx
}

// TestGetLastSignalForChecks_SkipsWorkerRows is the core contract: a beat
// followed by any number of worker-written evaluation rows must still resolve
// to the beat. GetLastResultForChecks, by design, returns the newest of them
// instead — both answers are asserted here so the two queries can never be
// collapsed into one by a later refactor.
func TestGetLastSignalForChecks_SkipsWorkerRows(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s, ctx := newSignalTestService(t)

	org := models.NewOrganization("signal-org", "Signal Org")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "beating", "heartbeat")
	r.NoError(s.CreateCheck(ctx, check))

	worker := newSignalWorker(t, s, ctx, "signal-w1")
	base := time.Now().Add(time.Hour)

	beat := seedSignalRow(t, s, ctx, org.UID, check.UID, models.ResultStatusUp, base, nil)

	// Three evaluation rows, all newer than the beat.
	var newestEvaluation *models.Result
	for i := 1; i <= 3; i++ {
		newestEvaluation = seedSignalRow(t, s, ctx, org.UID, check.UID,
			models.ResultStatusUp, base.Add(time.Duration(i)*time.Minute), &worker)
	}

	signals, err := s.GetLastSignalForChecks(ctx, org.UID, []string{check.UID})
	r.NoError(err)
	r.Len(signals, 1)
	r.Equal(beat.UID, signals[check.UID].UID,
		"the signal lookup must descend past every worker-written row to the beat")

	results, err := s.GetLastResultForChecks(ctx, org.UID, []string{check.UID})
	r.NoError(err)
	r.Equal(newestEvaluation.UID, results[check.UID].UID,
		"GetLastResultForChecks must keep returning the newest row of any origin (the API's lastResult)")
}

// TestGetLastSignalForChecks_SkipsCreatedMarker guards the one non-worker row
// that must never read as a signal: CreateCheck's one-time "Check created"
// marker has no worker_uid either, so only the status filter keeps it out.
func TestGetLastSignalForChecks_SkipsCreatedMarker(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s, ctx := newSignalTestService(t)

	org := models.NewOrganization("marker-org", "Marker Org")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "never-beaten", "heartbeat")
	r.NoError(s.CreateCheck(ctx, check))

	marker := new(models.Result)
	r.NoError(s.DB().NewSelect().Model(marker).Where("check_uid = ?", check.UID).Scan(ctx))
	r.NotNil(marker.Status)
	r.Equal(int(models.ResultStatusCreated), *marker.Status)
	r.Nil(marker.WorkerUID, "precondition: the creation marker carries no worker_uid")

	signals, err := s.GetLastSignalForChecks(ctx, org.UID, []string{check.UID})
	r.NoError(err)
	r.NotContains(signals, check.UID,
		"a check that was created but never beaten has no signal")

	// Once a real beat lands it wins, even though the marker is older.
	beat := seedSignalRow(t, s, ctx, org.UID, check.UID,
		models.ResultStatusUp, time.Now().Add(time.Hour), nil)

	signals, err = s.GetLastSignalForChecks(ctx, org.UID, []string{check.UID})
	r.NoError(err)
	r.Equal(beat.UID, signals[check.UID].UID)
}

// TestGetLastSignalForChecks_EvaluationOnlyCheckIsAbsent is the state a
// heartbeat reaches when its beats age out of raw retention: only evaluation
// rows remain, and the map must simply have no entry — which is what makes
// executePassiveJob fall through to "No heartbeat received".
func TestGetLastSignalForChecks_EvaluationOnlyCheckIsAbsent(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s, ctx := newSignalTestService(t)

	org := models.NewOrganization("eval-only-org", "Eval Only Org")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "silent", "heartbeat")
	r.NoError(s.CreateCheck(ctx, check))

	worker := newSignalWorker(t, s, ctx, "signal-w2")
	base := time.Now().Add(time.Hour)

	for i := 0; i < 5; i++ {
		seedSignalRow(t, s, ctx, org.UID, check.UID,
			models.ResultStatusDown, base.Add(time.Duration(i)*time.Minute), &worker)
	}

	// A reaper row (abandoned) is worker-written too and must fall out the
	// same way, with no status predicate of its own.
	seedSignalRow(t, s, ctx, org.UID, check.UID,
		models.ResultStatusAbandoned, base.Add(6*time.Minute), &worker)

	signals, err := s.GetLastSignalForChecks(ctx, org.UID, []string{check.UID})
	r.NoError(err)
	r.Empty(signals, "a check with only worker-written rows has no signal")
}

// TestGetLastSignalForChecks_OneEntryPerRequestedCheck mirrors the lastResult
// parity test: one entry per requested check at most, unknown uids simply
// absent, a repeated uid yielding one entry, and other organizations invisible.
func TestGetLastSignalForChecks_OneEntryPerRequestedCheck(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s, ctx := newSignalTestService(t)

	orgA := models.NewOrganization("signal-org-a", "Signal Org A")
	orgB := models.NewOrganization("signal-org-b", "Signal Org B")
	r.NoError(s.CreateOrganization(ctx, orgA))
	r.NoError(s.CreateOrganization(ctx, orgB))

	checkA := models.NewCheck(orgA.UID, "beat-a", "heartbeat")
	checkB := models.NewCheck(orgA.UID, "beat-b", "heartbeat")
	checkC := models.NewCheck(orgB.UID, "beat-c", "heartbeat")
	r.NoError(s.CreateCheck(ctx, checkA))
	r.NoError(s.CreateCheck(ctx, checkB))
	r.NoError(s.CreateCheck(ctx, checkC))

	base := time.Now().Add(time.Hour)
	seedSignalRow(t, s, ctx, orgA.UID, checkA.UID, models.ResultStatusUp, base, nil)
	newestA := seedSignalRow(t, s, ctx, orgA.UID, checkA.UID, models.ResultStatusUp, base.Add(time.Minute), nil)
	newestB := seedSignalRow(t, s, ctx, orgA.UID, checkB.UID, models.ResultStatusRunning, base, nil)
	seedSignalRow(t, s, ctx, orgB.UID, checkC.UID, models.ResultStatusUp, base, nil)

	signals, err := s.GetLastSignalForChecks(ctx, orgA.UID, []string{
		checkA.UID, checkA.UID, checkB.UID, checkC.UID,
		"00000000-0000-0000-0000-000000000000",
	})
	r.NoError(err)
	r.Len(signals, 2, "one entry per requested check that has a signal in THIS org")
	r.Equal(newestA.UID, signals[checkA.UID].UID)
	r.Equal(newestB.UID, signals[checkB.UID].UID)
	r.NotContains(signals, checkC.UID, "another organization's check must not leak")

	// The empty-input fast path returns an empty map, never nil.
	empty, err := s.GetLastSignalForChecks(ctx, orgA.UID, nil)
	r.NoError(err)
	r.NotNil(empty)
	r.Empty(empty)
}

// TestGetLastSignalForChecks_UsesTheSignalIndex asserts the lookup rides
// results_raw_signal_idx (migration 018) rather than results_raw_idx.
//
// This is the whole reason the partial index exists: results_raw_idx does not
// carry worker_uid, so for a check that has stopped beating the descent would
// walk every evaluation row inside raw retention — 1440 of them per tick at a
// 1-minute period with the 24 h default — on every single evaluation, for
// every silent check. EXPLAIN QUERY PLAN runs the exact SQL production
// executes (same package constant).
func TestGetLastSignalForChecks_UsesTheSignalIndex(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	s, ctx := newSignalTestService(t)

	org := models.NewOrganization("signal-plan-org", "Signal Plan Org")
	r.NoError(s.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "planned", "heartbeat")
	r.NoError(s.CreateCheck(ctx, check))

	worker := newSignalWorker(t, s, ctx, "signal-w3")
	base := time.Now().Add(time.Hour)
	seedSignalRow(t, s, ctx, org.UID, check.UID, models.ResultStatusUp, base, nil)

	for i := 1; i <= 40; i++ {
		seedSignalRow(t, s, ctx, org.UID, check.UID,
			models.ResultStatusUp, base.Add(time.Duration(i)*time.Minute), &worker)
	}

	_, err := s.DB().ExecContext(ctx, "ANALYZE")
	r.NoError(err)

	var plan []struct {
		ID      int    `bun:"id"`
		Parent  int    `bun:"parent"`
		NotUsed int    `bun:"notused"`
		Detail  string `bun:"detail"`
	}
	r.NoError(s.DB().NewRaw(
		"EXPLAIN QUERY PLAN "+lastSignalForChecksQuery,
		org.UID, int(models.ResultStatusCreated), bun.List([]string{check.UID}),
	).Scan(ctx, &plan))

	var builder strings.Builder
	for i := range plan {
		builder.WriteString(plan[i].Detail)
		builder.WriteString("\n")
	}
	steps := builder.String()

	r.Contains(steps, "results_raw_signal_idx",
		"the signal lookup must ride the dedicated partial index, not results_raw_idx:\n%s", steps)
	r.Contains(steps, "CORRELATED SCALAR SUBQUERY",
		"the winner per check must come from a correlated lookup, not a ranking:\n%s", steps)
	r.NotContains(steps, "SCAN",
		"no step may scan a row set; every step must be an index search:\n%s", steps)
}
