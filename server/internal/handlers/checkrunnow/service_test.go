package checkrunnow_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/checkrunnow"
)

type env struct {
	db  *sqlite.Service
	org *models.Organization
	svc *checkrunnow.Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("rn"+uuid.New().String()[:8], "Run Now")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	return &env{db: dbSvc, org: org, svc: checkrunnow.NewService(dbSvc, nil, nil)}
}

func (e *env) check(t *testing.T, slug, checkType string, regions []string, enabled bool) *models.Check {
	t.Helper()

	check := models.NewCheck(e.org.UID, slug, checkType)
	check.Regions = regions
	check.Enabled = enabled
	check.Period = 0
	check.Period = models.NewCheck(e.org.UID, "x", checkType).Period
	require.NoError(t, e.db.CreateCheck(t.Context(), check))

	return check
}

func (e *env) jobs(t *testing.T, checkUID string) map[string]*models.CheckJob {
	t.Helper()

	jobs, err := e.db.ListCheckJobsByCheckUID(t.Context(), checkUID)
	require.NoError(t, err)

	out := make(map[string]*models.CheckJob, len(jobs))

	for _, job := range jobs {
		region := ""
		if job.Region != nil {
			region = *job.Region
		}

		out[region] = job
	}

	return out
}

func (e *env) orgWindowCount(t *testing.T) float64 {
	t.Helper()

	entry, err := e.db.GetStateEntry(t.Context(), &e.org.UID, "run-now.org")
	require.NoError(t, err)

	if entry == nil || entry.Value == nil {
		return 0
	}

	return (*entry.Value)["count"].(float64) //nolint:forcetypeassert // test
}

func TestRunNowQueuesEveryRegion(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)
	check := e.check(t, "two", "http", []string{"eu", "us"}, true)

	resp, err := e.svc.RunNow(t.Context(), e.org.Slug, check.UID)
	r.NoError(err)
	r.Equal([]checkrunnow.RegionRun{
		{Region: "eu", Status: checkrunnow.StatusQueued},
		{Region: "us", Status: checkrunnow.StatusQueued},
	}, resp.Regions)

	for region, job := range e.jobs(t, check.UID) {
		r.WithinDuration(resp.RequestedAt, *job.ScheduledAt, time.Microsecond, region)
		r.WithinDuration(resp.RequestedAt, *job.EffectiveScheduledAt, time.Microsecond, region)
		r.Nil(job.CaptureRequestedAt, "run-now never forces a capture")
	}
}

func TestRunNowLeavesRunningJobsAlone(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		mark func(t *testing.T, e *env, job *models.CheckJob)
	}{
		{"leased", func(t *testing.T, e *env, job *models.CheckJob) {
			t.Helper()
			_, err := e.db.DB().NewUpdate().Model((*models.CheckJob)(nil)).
				Set("lease_expires_at = ?", time.Now().Add(time.Hour)).Where("uid = ?", job.UID).Exec(t.Context())
			require.NoError(t, err)
		}},
		{"multi-step run between slices", func(t *testing.T, e *env, job *models.CheckJob) {
			t.Helper()
			_, err := e.db.DB().NewUpdate().Model((*models.CheckJob)(nil)).
				Set("step_run_uid = ?", uuid.New().String()).
				Set("lease_expires_at = ?", time.Now().Add(-time.Minute)).
				Where("uid = ?", job.UID).Exec(t.Context())
			require.NoError(t, err)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			e := newEnv(t)
			check := e.check(t, "busy", "http", []string{"eu", "us"}, true)

			before := e.jobs(t, check.UID)
			tc.mark(t, e, before["eu"])

			resp, err := e.svc.RunNow(t.Context(), e.org.Slug, check.UID)
			r.NoError(err)
			r.Equal([]checkrunnow.RegionRun{
				{Region: "eu", Status: checkrunnow.StatusRunning},
				{Region: "us", Status: checkrunnow.StatusQueued},
			}, resp.Regions)

			after := e.jobs(t, check.UID)
			r.True(before["eu"].ScheduledAt.Equal(*after["eu"].ScheduledAt), "running job untouched")
			r.WithinDuration(resp.RequestedAt, *after["us"].ScheduledAt, time.Microsecond)
		})
	}
}

func TestRunNowRefusals(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)

	_, err := e.svc.RunNow(t.Context(), e.org.Slug, "missing")
	r.ErrorIs(err, checkrunnow.ErrCheckNotFound)

	_, err = e.svc.RunNow(t.Context(), "no-such-org", "missing")
	r.ErrorIs(err, checkrunnow.ErrOrganizationNotFound)

	off := e.check(t, "off", "http", []string{"eu"}, false)
	_, err = e.svc.RunNow(t.Context(), e.org.Slug, off.UID)
	r.ErrorIs(err, checkrunnow.ErrNoScheduledJob)
	r.InDelta(0, e.orgWindowCount(t), 0, "a refused request spends no budget")
}

func TestRunNowRateLimits(t *testing.T) {
	t.Parallel()

	t.Run("cheap type allows 3 per minute then refuses with a delay", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		e := newEnv(t)
		check := e.check(t, "web", "http", []string{"eu"}, true)

		for range 3 {
			_, err := e.svc.RunNow(t.Context(), e.org.Slug, check.UID)
			r.NoError(err)
		}

		_, err := e.svc.RunNow(t.Context(), e.org.Slug, check.UID)

		var limited *checkrunnow.RateLimitedError

		r.True(errors.As(err, &limited))
		r.Equal("check", limited.Scope)
		r.Positive(limited.RetryAfter)
		r.LessOrEqual(limited.RetryAfter, time.Minute)
	})

	t.Run("browser check refuses the second call", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		e := newEnv(t)
		check := e.check(t, "shot", "browser", []string{"eu"}, true)

		_, err := e.svc.RunNow(t.Context(), e.org.Slug, check.UID)
		r.NoError(err)

		_, err = e.svc.RunNow(t.Context(), e.org.Slug, check.UID)

		var limited *checkrunnow.RateLimitedError

		r.True(errors.As(err, &limited))
		r.Equal("check", limited.Scope)
	})

	t.Run("org window refuses across checks and does not burn the check window", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		e := newEnv(t)

		// Exhaust the org window directly, one admission short of refusal.
		for range checkrunnow.OrgLimit {
			_, _, err := e.db.AdmitFixedWindows(t.Context(), e.org.UID, []models.FixedWindow{
				{Key: "run-now.org", Limit: checkrunnow.OrgLimit, Window: checkrunnow.OrgWindow},
			}, time.Now())
			r.NoError(err)
		}

		check := e.check(t, "late", "http", []string{"eu"}, true)

		_, err := e.svc.RunNow(t.Context(), e.org.Slug, check.UID)

		var limited *checkrunnow.RateLimitedError

		r.True(errors.As(err, &limited))
		r.Equal("organization", limited.Scope)

		entry, err := e.db.GetStateEntry(t.Context(), &e.org.UID, "run-now.check."+check.UID)
		r.NoError(err)

		if entry != nil && entry.Value != nil {
			r.InDelta(0, (*entry.Value)["count"], 0, "atomic admission: the check window is not burned")
		}
	})
}

// TestRunNowThenWorkerClaim: the job a run-now made due is claimed at once by
// the express path, and a release does not lose the next tick.
func TestRunNowThenWorkerClaim(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := newEnv(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(ts.Close)

	check := models.NewCheck(e.org.UID, "live", "http")
	check.Regions = []string{"eu"}
	check.Config = models.JSONMap{"url": ts.URL}
	r.NoError(e.db.CreateCheck(t.Context(), check))

	jobsvc := checkjobsvc.NewService(e.db.DB())
	worker := models.NewWorker("w-"+uuid.New().String()[:8], "W")
	_, err := e.db.DB().NewInsert().Model(worker).Exec(t.Context())
	r.NoError(err)

	// Push the job's tick an hour away: nothing is due.
	far := time.Now().Add(time.Hour).UTC()
	_, err = e.db.DB().NewUpdate().Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", far).Set("effective_scheduled_at = ?", far).
		Where("check_uid = ?", check.UID).Exec(t.Context())
	r.NoError(err)

	region := "eu"
	claimed, err := jobsvc.ClaimJobsForCheck(t.Context(), worker.UID, &region, check.UID)
	r.NoError(err)
	r.Empty(claimed)

	resp, err := e.svc.RunNow(t.Context(), e.org.Slug, check.UID)
	r.NoError(err)

	claimed, err = jobsvc.ClaimJobsForCheck(t.Context(), worker.UID, &region, check.UID)
	r.NoError(err)
	r.Len(claimed, 1, "the run-now job is claimable at once")

	// While leased, a second request reports running and leaves it alone.
	_, err = e.svc.RunNow(t.Context(), e.org.Slug, check.UID)
	r.NoError(err)

	job := e.jobs(t, check.UID)["eu"]
	r.Equal(claimed[0].UID, job.UID)

	// A result stored after the request is what the dashboard matches with
	// periodStart >= requestedAt.
	result := models.NewResult(e.org.UID, check.UID, models.ResultStatusUp, 12)
	result.PeriodStart = resp.RequestedAt.Add(time.Second)
	r.NoError(e.db.CreateResult(t.Context(), result))

	listed, err := e.db.ListResults(t.Context(), &models.ListResultsFilter{
		OrganizationUID:  e.org.UID,
		CheckUIDs:        []string{check.UID},
		PeriodTypes:      []string{"raw"},
		PeriodStartAfter: &resp.RequestedAt,
	})
	r.NoError(err)
	r.Len(listed.Results, 1)
}
