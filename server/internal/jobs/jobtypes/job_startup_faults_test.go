package jobtypes

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/defaults"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// startupWorld is a fresh, migrated, empty database and a bare job context:
// what the startup job sees on the first boot of an install.
func startupWorld(t *testing.T) (*sqlite.Service, *jobdef.JobContext) {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = dbSvc.Close() })
	require.NoError(t, dbSvc.Initialize(ctx))

	return dbSvc, &jobdef.JobContext{
		DB:        dbSvc.DB(),
		DBService: dbSvc,
		Logger:    slog.New(slog.DiscardHandler),
	}
}

func countRows(t *testing.T, dbSvc *sqlite.Service, table string) int {
	t.Helper()

	count, err := dbSvc.DB().NewSelect().TableExpr(table).Count(t.Context())
	require.NoError(t, err)

	return count
}

// The first boot seeds the default organization, its admin and the sample
// checks. A store failure in a step the install cannot live without fails the
// startup job with an error naming that step; the audit event of a sample
// check is the one write it can lose.
func TestStartup_FirstBootStoreFailures(t *testing.T) {
	t.Parallel()

	controlDB, controlCtx := startupWorld(t)
	require.NoError(t, (&StartupJobRun{}).Run(t.Context(), controlCtx))

	wantChecks := countRows(t, controlDB, "checks")
	require.Positive(t, wantChecks, "positive control: a clean first boot seeds sample checks")

	tests := []struct {
		name    string
		fail    []string
		wantErr string
		orgs    int
		users   int
		members int
		checks  int
	}{
		{
			name:    "organization count",
			fail:    []string{"count(*)", `"organizations"`},
			wantErr: "failed to count organizations",
		},
		{
			name:    "default organization insert",
			fail:    []string{`INSERT INTO "organizations"`},
			wantErr: "failed to create default organization",
		},
		{
			name:    "admin user insert",
			fail:    []string{`INSERT INTO "users"`},
			wantErr: "failed to create admin user",
			orgs:    1,
		},
		{
			name:    "admin membership insert",
			fail:    []string{`INSERT INTO "organization_members"`},
			wantErr: "failed to create admin membership",
			orgs:    1, users: 1,
		},
		{
			name:    "samples.loaded read",
			fail:    []string{"SELECT", "samples.loaded"},
			wantErr: "failed to check samples.loaded parameter",
			orgs:    1, users: 1, members: 1,
		},
		{
			name:    "sample check insert",
			fail:    []string{`INSERT INTO "checks"`},
			wantErr: "failed to create sample check",
			orgs:    1, users: 1, members: 1,
		},
		{
			name:    "samples.loaded write",
			fail:    []string{"INSERT", "samples.loaded"},
			wantErr: "failed to set samples.loaded parameter",
			orgs:    1, users: 1, members: 1, checks: wantChecks,
		},
		{
			// The check exists; only its check.created event is lost.
			name: "sample check event insert is not fatal",
			fail: []string{`INSERT INTO "events"`},
			orgs: 1, users: 1, members: 1, checks: wantChecks,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			dbSvc, jctx := startupWorld(t)
			fault := failQueries(dbSvc.DB(), tt.fail...)

			err := (&StartupJobRun{}).Run(t.Context(), jctx)
			fault.requireFired(t)

			if tt.wantErr == "" {
				r.NoError(err)
			} else {
				r.ErrorContains(err, tt.wantErr)
				r.ErrorIs(err, context.Canceled, "the store error is wrapped, not replaced")
			}

			r.Equal(tt.orgs, countRows(t, dbSvc, "organizations"))
			r.Equal(tt.users, countRows(t, dbSvc, "users"))
			r.Equal(tt.members, countRows(t, dbSvc, "organization_members"))
			r.Equal(tt.checks, countRows(t, dbSvc, "checks"))
		})
	}
}

// Sample loading is guarded by the org parameter `samples.loaded`: a second
// pass creates nothing, so a user who deleted the samples does not get them
// back on the next boot.
func TestStartup_SamplesAreLoadedOnce(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	dbSvc, jctx := startupWorld(t)
	run := &StartupJobRun{}

	r.NoError(run.Run(t.Context(), jctx))

	admin, err := dbSvc.GetUserByEmail(t.Context(), defaults.Email)
	r.NoError(err)

	orgs, err := dbSvc.ListOrganizations(t.Context())
	r.NoError(err)
	r.Len(orgs, 1)
	r.NotNil(admin)

	seeded := countRows(t, dbSvc, "checks")
	r.Positive(seeded)

	_, err = dbSvc.DB().NewDelete().TableExpr("checks").Where("1 = 1").Exec(t.Context())
	r.NoError(err)

	r.NoError(run.loadSampleChecks(t.Context(), jctx, orgs[0].UID))
	r.Zero(countRows(t, dbSvc, "checks"), "samples are not seeded twice")
}

// A check type without a registered checker has no samples; it is skipped,
// not an error that would fail the boot.
func TestStartup_UnknownCheckerHasNoSamples(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	dbSvc, jctx := startupWorld(t)

	count, err := (&StartupJobRun{}).loadSamplesForChecker(t.Context(), jctx, "org",
		checkerdef.CheckType("no-such-checker"), &checkerdef.ListSampleOptions{Type: checkerdef.Default})
	r.NoError(err)
	r.Zero(count)
	r.Zero(countRows(t, dbSvc, "checks"))
}

// deafNotifier fails every Notify.
type deafNotifier struct {
	notifier.EventNotifier

	calls int
}

func (n *deafNotifier) Notify(context.Context, string, string) error {
	n.calls++

	return errTestBoom
}

// backfiller is a PrivateLocationMonitorBackfiller with a fixed answer.
type backfiller struct {
	created int
	err     error
	calls   int
}

func (b *backfiller) BackfillPrivateLocationMonitors(context.Context) (int, error) {
	b.calls++

	return b.created, b.err
}

// Waking the check runners and backfilling the private-location monitors are
// best effort: when they fail the boot goes on, the samples exist and every
// recurring job is still provisioned.
func TestStartup_BestEffortStepsDoNotFailTheBoot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		backfill *backfiller
	}{
		{"backfill fails", &backfiller{err: errTestBoom}},
		{"backfill creates monitors", &backfiller{created: 2}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			dbSvc, jctx := startupWorld(t)
			deaf := &deafNotifier{}
			jobs := &recordingJobs{}
			jctx.Services = &services.Registry{
				Jobs: jobs, EventNotifier: deaf, PrivateLocationMonitors: tt.backfill,
			}

			r.NoError((&StartupJobRun{}).Run(t.Context(), jctx))

			checks := countRows(t, dbSvc, "checks")
			r.Positive(checks)
			r.Equal(checks, deaf.calls, "every sample check tried to wake the runners")
			r.Equal(1, tt.backfill.calls)

			// The sweeps provisioned after the backfill are still there.
			for _, jobType := range []jobdef.JobType{
				jobdef.JobTypeAggregation, jobdef.JobTypeStateCleanup, jobdef.JobTypeUptimeReport,
				jobdef.JobTypeRegionHealthSweep, jobdef.JobTypePlatformWatchdog,
			} {
				r.Contains(jobs.calls, string(jobType))
			}
		})
	}
}
