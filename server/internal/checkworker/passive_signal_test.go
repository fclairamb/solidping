package checkworker

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
)

// Regression tests for spec 2026-09-02-03: executePassiveJob used to read
// "the newest raw row" (GetLastResultForChecks) rather than "the newest
// inbound signal" (GetLastSignalForChecks).
//
// That mattered because a passive check's raw history interleaves two kinds
// of row that only worker_uid tells apart:
//
//   - SIGNAL rows, written at ingest by handlers/heartbeat's recordBeat and
//     handlers/emailcheck — neither sets worker_uid or region;
//   - EVALUATION rows, written once per period by executePassiveJob itself
//     through DirectBackend.SubmitResult, which always stamps worker_uid.
//
// Reading the newest row of any origin made the evaluation re-anchor on its
// own predecessor from the second tick after a beat onwards. Every test in
// this file seeds BOTH kinds of row and asserts the beat wins; each one
// returns the opposite answer on the pre-fix code, which is the only thing
// that makes them regression tests rather than restatements of the switch.

// seedPassiveRow inserts one raw result for a passive check. workerUID nil
// makes it a SIGNAL row (what recordBeat / the email ingest write); non-nil
// makes it an EVALUATION row (what SubmitResult writes).
func seedPassiveRow(
	t *testing.T, dbSvc *sqlite.Service, ctx context.Context, //nolint:revive // ctx after t matches this package's helpers
	orgUID, checkUID string, status models.ResultStatus, at time.Time,
	workerUID *string, output models.JSONMap,
) *models.Result {
	t.Helper()

	rowUID, err := uuid.NewV7()
	require.NoError(t, err)

	statusInt := int(status)
	durationZero := float32(0)
	row := &models.Result{
		UID:             rowUID.String(),
		OrganizationUID: orgUID,
		CheckUID:        checkUID,
		PeriodType:      models.PeriodTypeRaw,
		PeriodStart:     at,
		WorkerUID:       workerUID,
		Status:          &statusInt,
		Duration:        &durationZero,
		Metrics:         make(models.JSONMap),
		Output:          output,
		CreatedAt:       at,
	}

	_, err = dbSvc.DB().NewInsert().Model(row).Exec(ctx)
	require.NoError(t, err)

	return row
}

// newPassiveEvaluationFixture builds an org, a worker, a passive check of the
// given type and its claimed job, ready for executePassiveJob. The check
// keeps models.NewCheck's default 1-minute period.
func newPassiveEvaluationFixture(
	t *testing.T, checkType checkerdef.CheckType,
) (*CheckWorker, *sqlite.Service, context.Context, *models.Organization, *models.Worker, *models.CheckJob) {
	t.Helper()

	runner, dbSvc, ctx := setupTestRunner(t)
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("passive-signal-org", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	worker := models.NewWorker("passive-worker", "Passive Signal Worker")
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)
	runner.setWorker(worker)

	check := models.NewCheck(org.UID, "passive-"+uuid.New().String()[:8], string(checkType))
	check.Config = models.JSONMap{"token": "passive-signal-token"}
	require.NoError(t, dbSvc.CreateCheck(ctx, check))
	require.Equal(t, time.Minute, time.Duration(check.Period),
		"these tests are written against the default 1-minute period")

	checkJob := new(models.CheckJob)
	require.NoError(t, dbSvc.DB().NewSelect().Model(checkJob).Where("check_uid = ?", check.UID).Scan(ctx))
	claimJobForTest(t, dbSvc, ctx, checkJob, worker.UID)

	return runner, dbSvc, ctx, org, worker, checkJob
}

// latestEvaluationRow returns the row executePassiveJob just wrote: the newest
// worker-written row that is not one of the seeded fixtures.
func latestEvaluationRow(
	t *testing.T, dbSvc *sqlite.Service, ctx context.Context, //nolint:revive // ctx after t matches this package's helpers
	checkUID string, excludeUIDs ...string,
) *models.Result {
	t.Helper()

	query := dbSvc.DB().NewSelect().
		Model((*models.Result)(nil)).
		Where("check_uid = ?", checkUID).
		Where("worker_uid IS NOT NULL").
		Order("period_start DESC").
		Limit(1)

	for _, uid := range excludeUIDs {
		query = query.Where("uid != ?", uid)
	}

	row := new(models.Result)
	require.NoError(t, query.Model(row).Scan(ctx))

	return row
}

// assertSignalAt parses an RFC3339 output field and compares it as an instant,
// not as a string: the DB round-trip can hand the timestamp back in a
// different location, which would make two renderings of the same moment
// differ textually.
func assertSignalAt(t *testing.T, output models.JSONMap, key string, want time.Time) {
	t.Helper()

	raw, ok := output[key]
	require.True(t, ok, "output must carry %q, got %v", key, output)

	str, ok := raw.(string)
	require.True(t, ok, "%s must be a string, got %T", key, raw)

	parsed, err := time.Parse(time.RFC3339, str)
	require.NoError(t, err, "%s must be RFC3339", key)

	// RFC3339 without fractional seconds truncates, hence the 1 s tolerance.
	assert.WithinDuration(t, want, parsed, time.Second,
		"%s must be the beat's own timestamp, not the previous evaluation's", key)
}

// parseDurationField reads a Duration.String()-rendered output field. It
// requires rather than type-asserts so a regression reports a failure instead
// of panicking the whole test binary (which would hide every sibling test).
func parseDurationField(t *testing.T, output models.JSONMap, key string) time.Duration {
	t.Helper()

	raw, ok := output[key]
	require.True(t, ok, "output must carry %q, got %v", key, output)

	str, ok := raw.(string)
	require.True(t, ok, "%s must be a string, got %T", key, raw)

	parsed, err := time.ParseDuration(str)
	require.NoError(t, err, "%s must parse as a Go duration", key)

	return parsed
}

// TestExecutePassiveJob_IgnoresItsOwnEvaluationRows is the core regression for
// spec 2026-09-02-03.
//
// A beat landed 90 s ago on a check with a 60 s period, so the check IS
// overdue by 30 s. An evaluation row written 10 s ago sits between the beat
// and now — exactly what the previous tick would have left behind.
//
// PRE-FIX this test FAILS: the evaluation reads its own 10 s-old row, computes
// elapsed = 10 s <= 60 s and writes "Heartbeat received" with status Up. It is
// that reading — elapsed measured between two consecutive evaluations rather
// than since the beat — that turned missing-beat detection into a per-tick
// coin flip on claim jitter.
//
//nolint:paralleltest // Test uses shared database state
func TestExecutePassiveJob_IgnoresItsOwnEvaluationRows(t *testing.T) {
	runner, dbSvc, ctx, org, worker, checkJob := newPassiveEvaluationFixture(t, checkerdef.CheckTypeHeartbeat)

	period := time.Duration(checkJob.Period)

	// The beat: no worker_uid, caller metadata, one period + 30 s ago.
	beatAt := time.Now().Add(-(period + 30*time.Second))
	beat := seedPassiveRow(t, dbSvc, ctx, org.UID, checkJob.CheckUID,
		models.ResultStatusUp, beatAt, nil,
		models.JSONMap{"message": "Heartbeat received", "callerIP": "203.0.113.9"})

	// The previous evaluation's own row: worker_uid set, 10 s ago.
	evaluation := seedPassiveRow(t, dbSvc, ctx, org.UID, checkJob.CheckUID,
		models.ResultStatusUp, time.Now().Add(-10*time.Second), &worker.UID,
		models.JSONMap{"message": "Heartbeat received", "lastSignalAt": beatAt.Format(time.RFC3339)})

	require.NoError(t, runner.executePassiveJob(ctx, runner.logger, checkJob))

	written := latestEvaluationRow(t, dbSvc, ctx, checkJob.CheckUID, evaluation.UID)

	require.NotNil(t, written.Status)
	assert.Equal(t, int(checkerdef.StatusDown), *written.Status,
		"a beat one period and a half old must be down, whatever the evaluator wrote 10s ago")
	assert.Equal(t, "Heartbeat overdue", written.Output["message"])
	assertSignalAt(t, written.Output, "lastSignalAt", beat.PeriodStart)

	overdueBy := parseDurationField(t, written.Output, "overdueBy")
	assert.InDelta(t, (30 * time.Second).Seconds(), overdueBy.Seconds(), 5,
		"overdueBy must be measured from the beat (~30s), not from the previous evaluation (~ms)")
}

// TestExecutePassiveJob_EmailIgnoresItsOwnEvaluationRows is the email variant:
// passiveSignalNoun shares the whole path, and the email ingest
// (handlers/emailcheck) writes signal rows with no worker_uid exactly like
// recordBeat does, so the same re-anchoring bug applied verbatim.
//
// PRE-FIX this test FAILS with "Email received" / status Up.
//
//nolint:paralleltest // Test uses shared database state
func TestExecutePassiveJob_EmailIgnoresItsOwnEvaluationRows(t *testing.T) {
	runner, dbSvc, ctx, org, worker, checkJob := newPassiveEvaluationFixture(t, checkerdef.CheckTypeEmail)

	period := time.Duration(checkJob.Period)

	signalAt := time.Now().Add(-(period + 30*time.Second))
	signal := seedPassiveRow(t, dbSvc, ctx, org.UID, checkJob.CheckUID,
		models.ResultStatusUp, signalAt, nil,
		models.JSONMap{"message": "Email received", "from": "alice@acme.com"})

	evaluation := seedPassiveRow(t, dbSvc, ctx, org.UID, checkJob.CheckUID,
		models.ResultStatusUp, time.Now().Add(-10*time.Second), &worker.UID,
		models.JSONMap{"message": "Email received", "lastSignalAt": signalAt.Format(time.RFC3339)})

	require.NoError(t, runner.executePassiveJob(ctx, runner.logger, checkJob))

	written := latestEvaluationRow(t, dbSvc, ctx, checkJob.CheckUID, evaluation.UID)

	require.NotNil(t, written.Status)
	assert.Equal(t, int(checkerdef.StatusDown), *written.Status)
	assert.Equal(t, "Email overdue", written.Output["message"],
		"the noun must still come from passiveSignalNoun")
	assertSignalAt(t, written.Output, "lastSignalAt", signal.PeriodStart)
}

// TestExecutePassiveJob_StaleRunDetectedDespiteEvaluationRows covers the
// branch that spec 2026-09-02-03 showed was unreachable in production.
//
// A "running" beat landed 2 periods + 30 s ago and never completed. One
// evaluation row (Running, written by the worker) sits one period ago. The
// StatusTimeout branch requires elapsed > 2×period measured from the BEAT.
//
// PRE-FIX this test FAILS: the evaluation reads its own Running row from one
// period ago, computes elapsed = 60 s <= 120 s and writes "Run in progress"
// again — which is why a stale run could never time out on a check that is
// being evaluated every period, i.e. every check.
// TestExecuteHeartbeatJob_RunningStatus only ever passed because it seeds a
// single old Running row with no evaluation in between.
//
//nolint:paralleltest // Test uses shared database state
func TestExecutePassiveJob_StaleRunDetectedDespiteEvaluationRows(t *testing.T) {
	runner, dbSvc, ctx, org, worker, checkJob := newPassiveEvaluationFixture(t, checkerdef.CheckTypeHeartbeat)

	period := time.Duration(checkJob.Period)

	runStartedAt := time.Now().Add(-(2*period + 30*time.Second))
	beat := seedPassiveRow(t, dbSvc, ctx, org.UID, checkJob.CheckUID,
		models.ResultStatusRunning, runStartedAt, nil,
		models.JSONMap{"message": "Run started"})

	evaluation := seedPassiveRow(t, dbSvc, ctx, org.UID, checkJob.CheckUID,
		models.ResultStatusRunning, time.Now().Add(-period), &worker.UID,
		models.JSONMap{"message": "Run in progress", "runStarted": runStartedAt.Format(time.RFC3339)})

	require.NoError(t, runner.executePassiveJob(ctx, runner.logger, checkJob))

	written := latestEvaluationRow(t, dbSvc, ctx, checkJob.CheckUID, evaluation.UID)

	require.NotNil(t, written.Status)
	assert.Equal(t, int(checkerdef.StatusTimeout), *written.Status,
		"a run started more than 2 periods ago has timed out, whatever the evaluator wrote a period ago")
	assert.Equal(t, "Run started but never completed", written.Output["message"])
	assertSignalAt(t, written.Output, "runStarted", beat.PeriodStart)
}

// TestExecutePassiveJob_CarriesLastSignalAtOnANonUpBeat pins D4's one
// behavioral addition: a beat that deliberately reports failure matches no
// branch of the switch, so the message stays the generic "No heartbeat
// received" (its wording is owned by spec 2026-09-02-04) — but the row now
// carries lastSignalAt, so the UI can say when the last signal landed instead
// of implying none was ever received.
//
//nolint:paralleltest // Test uses shared database state
func TestExecutePassiveJob_CarriesLastSignalAtOnANonUpBeat(t *testing.T) {
	runner, dbSvc, ctx, org, _, checkJob := newPassiveEvaluationFixture(t, checkerdef.CheckTypeHeartbeat)

	beat := seedPassiveRow(t, dbSvc, ctx, org.UID, checkJob.CheckUID,
		models.ResultStatusDown, time.Now().Add(-20*time.Second), nil,
		models.JSONMap{"message": "Job failed"})

	require.NoError(t, runner.executePassiveJob(ctx, runner.logger, checkJob))

	written := latestEvaluationRow(t, dbSvc, ctx, checkJob.CheckUID)

	require.NotNil(t, written.Status)
	assert.Equal(t, int(checkerdef.StatusDown), *written.Status)
	assert.Equal(t, "No heartbeat received", written.Output["message"],
		"the default branch's wording is owned by spec 2026-09-02-04 and must not drift here")
	assertSignalAt(t, written.Output, "lastSignalAt", beat.PeriodStart)
}

// TestExecutePassiveJob_CreationMarkerIsNotASignal guards the one row that
// also has no worker_uid but must never read as a beat: CreateCheck's
// one-time "Check created" marker. A brand-new heartbeat that has never been
// pinged must report "No heartbeat received" with no lastSignalAt at all.
//
//nolint:paralleltest // Test uses shared database state
func TestExecutePassiveJob_CreationMarkerIsNotASignal(t *testing.T) {
	runner, dbSvc, ctx, _, _, checkJob := newPassiveEvaluationFixture(t, checkerdef.CheckTypeHeartbeat)

	// Precondition: the only row the check has is its creation marker, and it
	// carries no worker_uid — so it is only the status filter that keeps it
	// out of the signal lookup.
	marker := new(models.Result)
	require.NoError(t, dbSvc.DB().NewSelect().
		Model(marker).
		Where("check_uid = ?", checkJob.CheckUID).
		Scan(ctx))
	require.NotNil(t, marker.Status)
	require.Equal(t, int(models.ResultStatusCreated), *marker.Status)
	require.Nil(t, marker.WorkerUID, "precondition: the creation marker has no worker_uid either")

	require.NoError(t, runner.executePassiveJob(ctx, runner.logger, checkJob))

	written := latestEvaluationRow(t, dbSvc, ctx, checkJob.CheckUID)

	require.NotNil(t, written.Status)
	assert.Equal(t, int(checkerdef.StatusDown), *written.Status)
	assert.Equal(t, "No heartbeat received", written.Output["message"])
	assert.NotContains(t, written.Output, "lastSignalAt",
		"the creation marker must not be reported as a signal")
}
