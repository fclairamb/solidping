package checks_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/checkworker/backend"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
)

// inflight is a check whose job a worker holds while the check is edited
// (spec 2026-10-12-05).
type inflight struct {
	svc     *checks.Service
	db      *sqlite.Service
	org     *models.Organization
	jobsvc  checkjobsvc.Service
	backend *backend.DirectBackend
	worker  *models.Worker
}

func newInflight(t *testing.T) *inflight {
	t.Helper()
	r := require.New(t)

	svc, dbSvc, org := newReconcileTestService(t)
	jobsvc := checkjobsvc.NewService(dbSvc.DB())

	worker := models.NewWorker("w-"+uuid.New().String()[:8], "W")
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(t.Context())
	r.NoError(err)

	be := backend.NewDirectBackend(dbSvc, jobsvc, incidents.NewService(dbSvc, nil, clock.Real{}, nil), nil, nil)

	return &inflight{svc: svc, db: dbSvc, org: org, jobsvc: jobsvc, backend: be, worker: worker}
}

// claim makes the check's job due and has the worker claim it.
func (f *inflight) claim(t *testing.T, checkUID string, region *string) *models.CheckJob {
	t.Helper()
	r := require.New(t)

	due := time.Now().Add(-time.Second).UTC()
	_, err := f.db.DB().NewUpdate().Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", due).Set("effective_scheduled_at = ?", due).
		Where("check_uid = ?", checkUID).Exec(t.Context())
	r.NoError(err)

	if region == nil {
		// A region-less passive job is claimed by the jobs node's evaluator,
		// not the regional claim: hold its lease directly.
		_, err = f.db.DB().NewUpdate().Model((*models.CheckJob)(nil)).
			Set("lease_worker_uid = ?", f.worker.UID).
			Set("lease_expires_at = ?", time.Now().Add(time.Hour).UTC()).
			Where("check_uid = ?", checkUID).Exec(t.Context())
		r.NoError(err)

		jobs, listErr := f.db.ListCheckJobsByCheckUID(t.Context(), checkUID)
		r.NoError(listErr)
		r.Len(jobs, 1)

		return jobs[0]
	}

	claimed, err := f.jobsvc.ClaimJobsForCheck(t.Context(), f.worker.UID, region, checkUID)
	r.NoError(err)
	r.Len(claimed, 1)

	return claimed[0]
}

// submit stores a result for the claimed (pre-edit) job the way the worker
// does, with the next tick it computed from the period it claimed.
func (f *inflight) submit(t *testing.T, job *models.CheckJob, output map[string]any) error {
	t.Helper()

	next := time.Now().Add(time.Duration(job.Period)).UTC()

	return f.backend.SubmitResult(t.Context(), job, f.worker.UID, &backend.SubmitResultRequest{
		Status:          int(models.ResultStatusUp),
		Duration:        1,
		Output:          output,
		Region:          job.Region,
		NextScheduledAt: next,
		ExecStart:       time.Now(),
		Sched:           &backend.SchedulingState{CostEWMAMs: 10, EffectiveScheduledAt: next},
	})
}

func (f *inflight) resultCount(t *testing.T, checkUID string) int {
	t.Helper()

	count, err := f.db.DB().NewSelect().Model((*models.Result)(nil)).
		Where("check_uid = ?", checkUID).Count(t.Context())
	require.NoError(t, err)

	return count
}

// TestPatchWhileLeased: a PATCH that changes the config or the period of a
// check whose job is mid-run keeps the job row (no delete and recreate), the
// worker's submission and release succeed, its result is stored, and the job
// is due at once so the edited config runs next.
func TestPatchWhileLeased(t *testing.T) {
	t.Parallel()

	period := "00:00:10"
	cases := []struct {
		name    string
		regions []string
		typ     string
		config  map[string]any
		patch   func() *checks.UpdateCheckRequest
	}{
		{
			name: "config change", regions: []string{"default"}, typ: "http",
			config: map[string]any{"url": "https://acme.com"},
			patch: func() *checks.UpdateCheckRequest {
				cfg := map[string]any{"url": "https://acme.com/health"}

				return &checks.UpdateCheckRequest{Config: &cfg}
			},
		},
		{
			name: "period change", regions: []string{"default"}, typ: "http",
			config: map[string]any{"url": "https://acme.com"},
			patch:  func() *checks.UpdateCheckRequest { return &checks.UpdateCheckRequest{Period: &period} },
		},
		{
			name: "region-less job, period change", typ: "heartbeat",
			patch: func() *checks.UpdateCheckRequest { return &checks.UpdateCheckRequest{Period: &period} },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			ctx := t.Context()
			f := newInflight(t)

			created, err := f.svc.CreateCheck(ctx, f.org.Slug, checks.CreateCheckRequest{
				Type: tc.typ, Config: tc.config, Regions: tc.regions, Period: strPtr("01:00:00"),
			})
			r.NoError(err)

			before, err := f.db.ListCheckJobsByCheckUID(ctx, created.UID)
			r.NoError(err)
			r.Len(before, 1)

			job := f.claim(t, created.UID, before[0].Region)
			results := f.resultCount(t, created.UID)

			_, err = f.svc.UpdateCheck(ctx, f.org.Slug, created.UID, tc.patch())
			r.NoError(err)

			// The in-flight run ends with the period it claimed (1 h).
			r.NoError(f.submit(t, job, nil), "no ErrJobClaimedByAnother")
			r.Equal(results+1, f.resultCount(t, created.UID), "the in-flight result is stored")

			after, err := f.db.ListCheckJobsByCheckUID(ctx, created.UID)
			r.NoError(err)
			r.Len(after, 1, "exactly one job row")
			r.Equal(job.UID, after[0].UID, "updated in place, not recreated")
			r.Nil(after[0].RunRequestedAt, "the release spent the re-run request")
			r.False(after[0].ScheduledAt.After(time.Now()), "the edited check runs next, not an old period later")

			if tc.patch().Period != nil {
				r.Equal(10*time.Second, time.Duration(after[0].Period), "the next run carries the new period")
			}

			if after[0].Region == nil {
				return
			}

			again, err := f.jobsvc.ClaimJobsForCheck(ctx, f.worker.UID, after[0].Region, created.UID)
			r.NoError(err)
			r.Len(again, 1, "the edited check is claimable at once")
		})
	}
}

// TestDeleteWhileLeased: a check deleted mid-run keeps the in-flight result,
// and the worker's release reports nothing wrong (no "no rows updated").
func TestDeleteWhileLeased(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	f := newInflight(t)

	created, err := f.svc.CreateCheck(ctx, f.org.Slug, checks.CreateCheckRequest{
		Type: "http", Config: map[string]any{"url": "https://acme.com"},
		Regions: []string{"default"}, Period: strPtr("00:00:10"),
	})
	r.NoError(err)

	region := "default"
	job := f.claim(t, created.UID, &region)
	results := f.resultCount(t, created.UID)

	r.NoError(f.svc.DeleteCheck(ctx, f.org.Slug, created.UID))

	left, err := f.db.ListCheckJobsByCheckUID(ctx, created.UID)
	r.NoError(err)
	r.Empty(left, "control: the job row is gone under the lease")

	r.NoError(f.submit(t, job, nil), "the release of a deleted job is the normal end of it")
	r.Equal(results+1, f.resultCount(t, created.UID), "the in-flight result is stored")
}

// TestPatchIdleJobIsNotRerun is the control: an edit on a job nobody holds
// parks nothing and leaves its schedule alone.
func TestPatchIdleJobIsNotRerun(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	f := newInflight(t)

	created, err := f.svc.CreateCheck(ctx, f.org.Slug, checks.CreateCheckRequest{
		Type: "http", Config: map[string]any{"url": "https://acme.com"},
		Regions: []string{"default"}, Period: strPtr("01:00:00"),
	})
	r.NoError(err)

	cfg := map[string]any{"url": "https://acme.com/health"}
	_, err = f.svc.UpdateCheck(ctx, f.org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &cfg})
	r.NoError(err)

	jobs, err := f.db.ListCheckJobsByCheckUID(ctx, created.UID)
	r.NoError(err)
	r.Len(jobs, 1)
	r.Nil(jobs[0].RunRequestedAt)
}

// TestDNSBaselinePatchMidRun is the DNS E2E flake of spec 2026-10-12-05: the
// baseline is edited (and the period shortened) while the run that captured
// the first baseline still holds the job. The next run is due at once and
// reports the change.
func TestDNSBaselinePatchMidRun(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()
	f := newInflight(t)

	created, err := f.svc.CreateCheck(ctx, f.org.Slug, checks.CreateCheckRequest{
		Type:    "dns",
		Config:  map[string]any{"host": "localhost", "record_type": "A", "detect_changes": true},
		Regions: []string{"default"},
		Period:  strPtr("01:00:00"),
	})
	r.NoError(err)

	region := "default"
	job := f.claim(t, created.UID, &region)
	results := f.resultCount(t, created.UID)

	// The first run's answer is captured as the baseline, then the test (or a
	// user) edits it before the run has released its lease.
	written, err := f.db.CaptureCheckConfigBaseline(ctx, created.UID, region, []string{"127.0.0.1"})
	r.NoError(err)
	r.True(written)

	period := "00:00:10"
	cfg := map[string]any{
		"host": "localhost", "record_type": "A", "detect_changes": true, "on_change": "warning",
		"baseline": map[string]any{region: []any{"10.9.9.9"}},
	}
	_, err = f.svc.UpdateCheck(ctx, f.org.Slug, created.UID, &checks.UpdateCheckRequest{
		Config: &cfg, Period: &period,
	})
	r.NoError(err)

	r.NoError(f.submit(t, job, nil))

	// Within two periods (here: at once) the next run is claimed with the new
	// baseline, and its result carries the change.
	next, err := f.jobsvc.ClaimJobsForCheck(ctx, f.worker.UID, &region, created.UID)
	r.NoError(err)
	r.Len(next, 1, "the edited check runs right after the in-flight run")

	result := runDNS(ctx, t, next[0])
	r.Equal(map[string]any{"added": []string{"127.0.0.1"}, "removed": []string{"10.9.9.9"}}, result.Output["changes"])

	r.NoError(f.submit(t, next[0], result.Output))

	check, err := f.db.GetCheck(ctx, f.org.UID, created.UID)
	r.NoError(err)
	r.NotNil(check)
	r.Equal(results+2, f.resultCount(t, created.UID))
}

// runDNS executes a claimed dns job the way the worker does.
func runDNS(ctx context.Context, t *testing.T, job *models.CheckJob) *checkerdef.Result {
	t.Helper()
	r := require.New(t)

	cfg, ok := registry.ParseConfig(checkerdef.CheckTypeDNS)
	r.True(ok)
	r.NoError(cfg.FromMap(job.Config))

	selector, ok := cfg.(interface{ SelectRegion(region string) })
	r.True(ok)
	selector.SelectRegion(*job.Region)

	checker, ok := registry.GetChecker(checkerdef.CheckTypeDNS)
	r.True(ok)

	result, err := checker.Execute(ctx, cfg)
	r.NoError(err)

	return result
}
