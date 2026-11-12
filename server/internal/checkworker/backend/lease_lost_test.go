package backend_test

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// TestSubmitResultCountsLostLease wires spec 2026-11-11-06 end to end on the
// in-process path: a run whose lease expired and was re-claimed by a peer
// still stores its result, its release fails with ErrJobClaimedByAnother, and
// the loss is counted for the result's region and logged as a WARN.
//
//nolint:paralleltest // captureDefaultLogs swaps the process-global logger
func TestSubmitResultCountsLostLease(t *testing.T) {
	logs := captureDefaultLogs(t)
	be, _, dbSvc, ctx := newDirectBackend(t)

	org := newOrg(t, ctx, dbSvc, "lease-lost")
	check := seedJob(t, ctx, dbSvc, org, "lease-lost", publicOnly(), "")
	workerA := registerWorker(ctx, t, dbSvc, "wk-lease-a")
	workerB := registerWorker(ctx, t, dbSvc, "wk-lease-b")

	job := claimOne(ctx, t, be, workerA, check.UID)
	require.NotNil(t, job)
	require.NotNil(t, job.ClaimedAt)

	// The run outlives the lease and the peer re-claims the job.
	_, err := dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("lease_expires_at = ?", time.Now().Add(-time.Second)).
		Where("uid = ?", job.UID).
		Exec(ctx)
	require.NoError(t, err)
	require.NotNil(t, claimOne(ctx, t, be, workerB, check.UID))

	region := "lease-lost-region"
	before := testutil.ToFloat64(prommetrics.CheckLeaseLost.WithLabelValues(region))

	req := submitReq()
	req.Region = &region

	err = be.SubmitResult(ctx, job, workerA, req)
	require.ErrorIs(t, err, checkjobsvc.ErrJobClaimedByAnother)

	require.InDelta(t, 1.0, testutil.ToFloat64(prommetrics.CheckLeaseLost.WithLabelValues(region))-before, 0)
	require.Contains(t, logs.String(), "Check lease lost")
	require.Contains(t, logs.String(), "missed_by=")
}
