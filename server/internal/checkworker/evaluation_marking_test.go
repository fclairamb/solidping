package checkworker

import (
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Tests for spec 2026-09-02-04: a passive check's raw history interleaves
// beats (written at ingest, carrying caller metadata, no worker/region) and
// scheduler evaluations (written every period by executePassiveJob). Both used
// to read "Heartbeat received" with status Up, so a user who opened the
// evaluation row that landed seconds after their ping saw no Caller card and
// concluded the ping had not been recorded.
//
// Two halves are under test here, and the second is the one that matters:
//
//  1. every evaluation row declares itself (`evaluation: true`) and points at
//     the beat it read (`lastSignalAt`, `lastSignalResultUid`), on every
//     branch of the switch;
//  2. a beat row is stored byte-for-byte as it is today. That is the POSITIVE
//     CONTROL — the whole design rests on "the marker appears on evaluation
//     rows and NOWHERE else", so a test that only checked the evaluation side
//     would pass just as happily if recordBeat had started stamping the marker
//     too, which would destroy the distinction the spec exists to create.

// heartbeatBeatOutput is a beat's stored output, shaped exactly like
// handlers/heartbeat's buildHeartbeatOutput builds it: the message plus the
// server-observed caller metadata plus the caller's own nested body.
//
// Every value is a plain JSON-decodable Go value (float64 for the number, a
// bare map for the nested object) so a DB round-trip is an identity: the
// deep-compare below must fail because the row CHANGED, never because JSON
// decoding handed back a different-but-equivalent representation.
func heartbeatBeatOutput(message string) models.JSONMap {
	return models.JSONMap{
		"message":    message,
		"userAgent":  "curl/8.4.0",
		"remoteAddr": "203.0.113.9",
		"httpMethod": "POST",
		"data": map[string]any{
			"runUrl":      "https://ci.acme.com/runs/512",
			"recordCount": float64(18234),
		},
	}
}

// assertMarkedEvaluation asserts the row executePassiveJob just wrote is a
// worker-written row that declares itself as an evaluation.
func assertMarkedEvaluation(t *testing.T, written *models.Result) {
	t.Helper()

	require.NotNil(t, written.WorkerUID, "an evaluation row is always written by a worker")
	assert.Equal(t, true, written.Output[outputKeyEvaluation],
		"every evaluation row must declare itself, on every branch of the switch")
}

// TestExecutePassiveJob_MarksEvaluationRows is the main table: for both passive
// check types, a beat that arrived inside the period must produce an evaluation
// row that says so IN ITS OWN WORDS ("Heartbeat on time", not the ingest's
// "Heartbeat received"), declares itself, and points back at the beat — while
// the beat itself is left exactly as it was found.
//
//nolint:paralleltest // Test uses shared database state
func TestExecutePassiveJob_MarksEvaluationRows(t *testing.T) {
	tests := []struct {
		name        string
		checkType   checkerdef.CheckType
		beatMessage string
		wantMessage string
	}{
		{
			name:        "heartbeat",
			checkType:   checkerdef.CheckTypeHeartbeat,
			beatMessage: "Heartbeat received",
			wantMessage: "Heartbeat on time",
		},
		{
			name:        "email",
			checkType:   checkerdef.CheckTypeEmail,
			beatMessage: "Email received",
			wantMessage: "Email on time",
		},
	}

	for _, tt := range tests {
		//nolint:paralleltest // Test uses shared database state
		t.Run(tt.name, func(t *testing.T) {
			runner, dbSvc, ctx, org, _, checkJob := newPassiveEvaluationFixture(t, tt.checkType)

			// The beat: no worker_uid, caller metadata, 10 s ago — well inside
			// the check's 1-minute period.
			seededOutput := heartbeatBeatOutput(tt.beatMessage)
			beat := seedPassiveRow(t, dbSvc, ctx, org.UID, checkJob.CheckUID,
				models.ResultStatusUp, time.Now().Add(-10*time.Second), nil,
				// Hand the seeder a copy: if the insert (or anything else)
				// mutated the map we passed in, comparing against that same
				// map afterwards would compare a value to itself and prove
				// nothing.
				maps.Clone(seededOutput))

			require.NoError(t, runner.executePassiveJob(ctx, runner.logger, checkJob))

			written := latestEvaluationRow(t, dbSvc, ctx, checkJob.CheckUID)

			require.NotNil(t, written.Status)
			assert.Equal(t, int(checkerdef.StatusUp), *written.Status)
			assertMarkedEvaluation(t, written)

			assert.Equal(t, tt.wantMessage, written.Output[outputKeyMessage],
				"the evaluation must not reuse the ingest's wording — that collision is the bug")
			assertSignalAt(t, written.Output, outputKeyLastSignalAt, beat.PeriodStart)
			assert.Equal(t, beat.UID, written.Output[outputKeyLastSignalResultUID],
				"the evaluation must point at the beat it read, so the UI can link to it")

			// POSITIVE CONTROL. Re-read the beat and deep-compare its stored
			// output against exactly what was inserted. This fails the moment
			// anything starts marking ingest rows — which would make
			// `evaluation` useless as the "is this a real signal?" test that
			// dash0, the REST API and MCP consumers all key off.
			reread := new(models.Result)
			require.NoError(t, dbSvc.DB().NewSelect().
				Model(reread).
				Where("uid = ?", beat.UID).
				Scan(ctx))

			assert.NotContains(t, reread.Output, outputKeyEvaluation,
				"an ingested beat must NEVER carry the evaluation marker")
			assert.Equal(t, map[string]any(seededOutput), map[string]any(reread.Output),
				"the beat row must be stored byte-for-byte as it is today: message and every caller key intact")
		})
	}
}

// TestExecutePassiveJob_MarksEveryBranch walks the rest of the switch. The
// marker is stamped after the switch precisely so no branch can forget it, and
// this is what proves it — including the branches that produce a failing status
// and the one that has no signal to point at.
//
//nolint:paralleltest // Test uses shared database state
func TestExecutePassiveJob_MarksEveryBranch(t *testing.T) {
	tests := []struct {
		name string
		// seed describes the beat to plant; nil means "plant nothing".
		seedStatus  *models.ResultStatus
		seedAgeMul  float64 // beat age as a multiple of the check's period
		wantStatus  checkerdef.Status
		wantMessage string
		wantSignal  bool // does the evaluation point at a beat?
	}{
		{
			name:        "overdue beat",
			seedStatus:  statusPtr(models.ResultStatusUp),
			seedAgeMul:  1.5,
			wantStatus:  checkerdef.StatusDown,
			wantMessage: "Heartbeat overdue",
			wantSignal:  true,
		},
		{
			name:        "no signal at all",
			seedStatus:  nil,
			wantStatus:  checkerdef.StatusDown,
			wantMessage: "No heartbeat received",
			wantSignal:  false,
		},
		{
			name:        "running inside the grace window",
			seedStatus:  statusPtr(models.ResultStatusRunning),
			seedAgeMul:  0.5,
			wantStatus:  checkerdef.StatusRunning,
			wantMessage: "Run in progress",
			wantSignal:  true,
		},
		{
			name:        "stale run past the grace window",
			seedStatus:  statusPtr(models.ResultStatusRunning),
			seedAgeMul:  2.5,
			wantStatus:  checkerdef.StatusTimeout,
			wantMessage: "Run started but never completed",
			wantSignal:  true,
		},
		{
			name:        "beat reported failure",
			seedStatus:  statusPtr(models.ResultStatusDown),
			seedAgeMul:  0.2,
			wantStatus:  checkerdef.StatusDown,
			wantMessage: "Last heartbeat reported failure",
			wantSignal:  true,
		},
		{
			name:        "beat reported error",
			seedStatus:  statusPtr(models.ResultStatusError),
			seedAgeMul:  0.2,
			wantStatus:  checkerdef.StatusDown,
			wantMessage: "Last heartbeat reported error",
			wantSignal:  true,
		},
	}

	for _, tt := range tests {
		//nolint:paralleltest // Test uses shared database state
		t.Run(tt.name, func(t *testing.T) {
			runner, dbSvc, ctx, org, _, checkJob := newPassiveEvaluationFixture(t, checkerdef.CheckTypeHeartbeat)

			period := time.Duration(checkJob.Period)

			var beat *models.Result
			if tt.seedStatus != nil {
				at := time.Now().Add(-time.Duration(float64(period) * tt.seedAgeMul))
				beat = seedPassiveRow(t, dbSvc, ctx, org.UID, checkJob.CheckUID,
					*tt.seedStatus, at, nil, models.JSONMap{"message": "beat"})
			}

			require.NoError(t, runner.executePassiveJob(ctx, runner.logger, checkJob))

			written := latestEvaluationRow(t, dbSvc, ctx, checkJob.CheckUID)

			require.NotNil(t, written.Status)
			assert.Equal(t, int(tt.wantStatus), *written.Status)
			assert.Equal(t, tt.wantMessage, written.Output[outputKeyMessage])
			assertMarkedEvaluation(t, written)

			if tt.wantSignal {
				assertSignalAt(t, written.Output, outputKeyLastSignalAt, beat.PeriodStart)
				assert.Equal(t, beat.UID, written.Output[outputKeyLastSignalResultUID])
			} else {
				assert.NotContains(t, written.Output, outputKeyLastSignalAt,
					"with no signal on record there is nothing to point at")
				assert.NotContains(t, written.Output, outputKeyLastSignalResultUID)
			}
		})
	}
}

func statusPtr(s models.ResultStatus) *models.ResultStatus {
	return &s
}
