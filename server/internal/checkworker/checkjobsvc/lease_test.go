package checkjobsvc_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// TestLeaseDuration pins the sizing of spec 2026-11-11-06:
// max(period, check timeout) + margin, the check timeout being the job's own
// `timeout` (clamped at 30s) or the deployment default.
func TestLeaseDuration(t *testing.T) {
	t.Parallel()

	job := func(period time.Duration, config models.JSONMap) *models.CheckJob {
		return &models.CheckJob{Period: timeutils.Duration(period), Config: config}
	}

	cases := []struct {
		name string
		job  *models.CheckJob
		want time.Duration
	}{
		{"period wins over the default timeout", job(time.Minute, nil), time.Minute + 30*time.Second},
		{"default timeout wins over a short period", job(5*time.Second, nil), 15*time.Second + 30*time.Second},
		{"own timeout wins over a short period", job(10*time.Second, models.JSONMap{"timeout": "25s"}), 55 * time.Second},
		{"own timeout is clamped at 30s", job(10*time.Second, models.JSONMap{"timeout": "2m"}), time.Minute},
		{"unparsable timeout falls back", job(5*time.Second, models.JSONMap{"timeout": "soon"}), 45 * time.Second},
	}

	for _, tc := range cases {
		require.Equal(t, tc.want,
			checkjobsvc.LeaseDuration(tc.job, checkjobsvc.DefaultLeaseCheckTimeout, checkjobsvc.DefaultLeaseMargin),
			tc.name)
	}

	require.Equal(t, 10*time.Second+2*time.Minute,
		checkjobsvc.LeaseDuration(job(10*time.Second, nil), 5*time.Second, 2*time.Minute),
		"the margin is the deployment's")
}

//nolint:revive // Test helper function, context parameter order is acceptable
func setTestCheckJobTimeout(t *testing.T, ctx context.Context, svc *sqlite.Service, jobUID, timeout string) {
	t.Helper()

	_, err := svc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("config = ?", models.JSONMap{"timeout": timeout}).
		Where("uid = ?", jobUID).
		Exec(ctx)
	require.NoError(t, err)
}

// TestLeaseExpiredBetweenClaimAndRelease is the scenario of
// fclairamb/solidping-business#63: a slow remote region whose run outlives
// its lease. While the lease (sized on the check's timeout and the
// deployment's margin) is valid, a peer's claim is refused; once it has
// expired, the peer re-claims the job, the first worker's release fails with
// ErrJobClaimedByAnother, and that failure is counted and logged with what the
// lease missed by.
//
//nolint:paralleltest // Test shares database state and a global counter
func TestLeaseExpiredBetweenClaimAndRelease(t *testing.T) {
	dbSvc, ctx := setupTestDB(t)
	defer func() { _ = dbSvc.Close() }()

	margin := 2 * time.Minute
	svc := checkjobsvc.NewService(dbSvc.DB(), checkjobsvc.WithLeaseMargin(margin))
	org := createTestOrg(t, ctx, dbSvc)
	workerA := createTestWorker(t, ctx, dbSvc, nil)
	workerB := createTestWorker(t, ctx, dbSvc, nil)

	now := time.Now()
	job := createTestCheckJob(t, ctx, dbSvc, org.UID, now.Add(-time.Second), nil)
	setTestCheckJobPeriod(t, ctx, dbSvc, job.UID, 10*time.Second)
	setTestCheckJobTimeout(t, ctx, dbSvc, job.UID, "25s")

	jobsA, _, err := svc.ClaimJobs(ctx, workerA.UID, nil, 10, 10, 5*time.Minute)
	require.NoError(t, err)

	var claimed *models.CheckJob

	for _, j := range jobsA {
		if j.UID == job.UID {
			claimed = j
		}
	}

	require.NotNil(t, claimed)
	require.NotNil(t, claimed.ClaimedAt, "the claim stamps its own time for the held histogram")
	require.NotNil(t, claimed.LeaseExpiresAt)
	// Sized on the 25s timeout, not the 10s period, plus the configured margin.
	require.WithinDuration(t, claimed.ClaimedAt.Add(25*time.Second+margin), *claimed.LeaseExpiresAt, time.Second)

	// Long past the old period + 30s lease (40s), the new one still holds:
	// the peer is refused.
	_, err = dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", now.Add(-time.Minute)).
		Where("uid = ?", job.UID).
		Exec(ctx)
	require.NoError(t, err)

	jobsB, _, err := svc.ClaimJobs(ctx, workerB.UID, nil, 10, 10, 5*time.Minute)
	require.NoError(t, err)
	require.Nil(t, claimedJob(jobsB, job.UID), "a valid lease must refuse the peer's claim")

	// The run outlives the lease: it expires, the peer re-claims.
	_, err = dbSvc.DB().NewUpdate().
		Model((*models.CheckJob)(nil)).
		Set("lease_expires_at = ?", time.Now().Add(-time.Second)).
		Where("uid = ?", job.UID).
		Exec(ctx)
	require.NoError(t, err)

	jobsB, _, err = svc.ClaimJobs(ctx, workerB.UID, nil, 10, 10, 5*time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimedJob(jobsB, job.UID), "an expired lease is re-claimed by the peer")

	// The first worker's release is refused, counted and logged.
	region := "lease-test-region"
	before := testutil.ToFloat64(prommetrics.CheckLeaseLost.WithLabelValues(region))

	releaseErr := svc.ReleaseLease(ctx, job.UID, workerA.UID, time.Now().Add(10*time.Second))
	require.ErrorIs(t, releaseErr, checkjobsvc.ErrJobClaimedByAnother)

	logs := &bytes.Buffer{}
	checkjobsvc.ObserveRelease(ctx, slog.New(slog.NewTextHandler(logs, nil)), claimed, region, releaseErr)

	require.InDelta(t, 1.0, testutil.ToFloat64(prommetrics.CheckLeaseLost.WithLabelValues(region))-before, 0)
	require.Contains(t, logs.String(), "level=WARN")
	require.Contains(t, logs.String(), "Check lease lost")
	require.Contains(t, logs.String(), "lease_expires_at=")
	require.Contains(t, logs.String(), "missed_by=")
	require.Contains(t, logs.String(), "period=10s")
	require.Contains(t, logs.String(), "timeout=25s")

	// The peer's own release still succeeds and is not counted as lost.
	var peerJob *models.CheckJob

	for _, j := range jobsB {
		if j.UID == job.UID {
			peerJob = j
		}
	}

	require.NotNil(t, peerJob)

	peerErr := svc.ReleaseLease(ctx, job.UID, workerB.UID, time.Now().Add(10*time.Second))
	require.NoError(t, peerErr)
	checkjobsvc.ObserveRelease(ctx, slog.New(slog.NewTextHandler(logs, nil)), peerJob, region, peerErr)
	require.InDelta(t, 1.0, testutil.ToFloat64(prommetrics.CheckLeaseLost.WithLabelValues(region))-before, 0)
	require.Positive(t, testutil.CollectAndCount(prommetrics.CheckLeaseHeld, "solidping_check_lease_held_seconds"),
		"the claim-to-release time is observed")
}

// claimedJob returns the job of jobs whose UID is uid, nil when there is none.
func claimedJob(jobs []*models.CheckJob, uid string) *models.CheckJob {
	for _, j := range jobs {
		if j.UID == uid {
			return j
		}
	}

	return nil
}
