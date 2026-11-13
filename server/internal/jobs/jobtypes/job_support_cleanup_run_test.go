package jobtypes

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/support"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

// supportInbox is an inbox holding five threads: three closed 400 days ago,
// one closed 10 days ago and one still open after 400 days.
type supportInbox struct {
	db   *sqlite.Service
	jobs *recordingJobs
	jctx *jobdef.JobContext
}

func newSupportInbox(t *testing.T) *supportInbox {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	seed := func(identity, status string, age time.Duration) {
		thread := models.NewSupportThread("email", identity, time.Now().Add(-age))
		thread.Subject = "help"
		thread.Status = status

		_, insertErr := dbSvc.DB().NewInsert().Model(thread).Exec(ctx)
		require.NoError(t, insertErr)
	}

	const day = 24 * time.Hour

	seed("alice@acme.com", models.SupportStatusClosed, 400*day)
	seed("bob@acme.com", models.SupportStatusClosed, 400*day)
	seed("carol@acme.com", models.SupportStatusClosed, 400*day)
	seed("dave@acme.com", models.SupportStatusClosed, 10*day)
	seed("erin@acme.com", models.SupportStatusOpen, 400*day)

	jobs := &recordingJobs{}

	return &supportInbox{
		db: dbSvc, jobs: jobs,
		jctx: &jobdef.JobContext{
			DBService: dbSvc,
			Logger:    slog.New(slog.DiscardHandler),
			Services:  &services.Registry{Support: support.NewService(dbSvc, support.Options{}), Jobs: jobs},
		},
	}
}

func (s *supportInbox) threads(t *testing.T) int {
	t.Helper()

	count, err := s.db.DB().NewSelect().TableExpr("support_threads").Count(t.Context())
	require.NoError(t, err)

	return count
}

// The retention window comes from the `support.retention_days` system
// parameter, 365 days when unset or unusable, and ZERO means keep forever.
// Only closed threads older than the window go; the sweep re-arms each time.
func TestSupportCleanup_PurgesClosedThreadsPastRetention(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// retention is the stored system parameter; nil leaves it unset.
		retention any
		run       SupportCleanupJobRun
		left      int
	}{
		{name: "default retention is a year", left: 2},
		{name: "a shorter window purges the recently closed thread too", retention: 5, left: 1},
		{name: "zero keeps every thread", retention: 0, left: 5},
		{name: "an unusable value falls back to the default", retention: "forever", left: 2},
		{name: "small batches drain the backlog in one run", run: SupportCleanupJobRun{batchSize: 2}, left: 2},
		{
			// The batch cap bounds one run; the next run takes the rest.
			name: "the batch cap bounds a single run",
			run:  SupportCleanupJobRun{batchSize: 2, maxBatches: 1},
			left: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			inbox := newSupportInbox(t)

			if tt.retention != nil {
				r.NoError(inbox.db.SetSystemParameter(t.Context(),
					string(systemconfig.KeySupportRetentionDays), tt.retention, false))
			}

			run := tt.run
			r.NoError(run.Run(t.Context(), inbox.jctx))

			r.Equal(tt.left, inbox.threads(t))
			r.Equal([]string{string(jobdef.JobTypeSupportCleanup)}, inbox.jobs.calls, "the sweep re-arms itself")
		})
	}
}

// A purge that fails deletes nothing and asks for a retry.
func TestSupportCleanup_PurgeFailureIsRetryable(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	inbox := newSupportInbox(t)
	fault := failQueries(inbox.db.DB(), `FROM "support_threads"`, "status = 'closed'")

	err := (&SupportCleanupJobRun{}).Run(t.Context(), inbox.jctx)
	fault.requireFired(t)

	r.ErrorIs(err, context.Canceled)
	r.True(jobdef.IsRetryable(err))
	r.Equal(5, inbox.threads(t))
}

// An instance without the support inbox has nothing to purge; the sweep
// stays scheduled so enabling the inbox later needs no restart.
func TestSupportCleanup_WithoutInbox(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	inbox := newSupportInbox(t)
	inbox.jctx.Services.Support = nil

	r.NoError((&SupportCleanupJobRun{}).Run(t.Context(), inbox.jctx))
	r.Equal(5, inbox.threads(t))
	r.Equal([]string{string(jobdef.JobTypeSupportCleanup)}, inbox.jobs.calls)

	// A failure to re-arm is logged, never returned.
	inbox.jobs.err = errTestBoom
	r.NoError((&SupportCleanupJobRun{}).Run(t.Context(), inbox.jctx))
	r.Len(inbox.jobs.calls, 2)

	inbox.jctx.Services = nil
	r.NoError((&SupportCleanupJobRun{}).Run(t.Context(), inbox.jctx))
}

// SP_SUPPORT_RETENTION_DAYS wins over the stored parameter; a value that is
// not a non-negative integer is ignored.
func TestSupportCleanup_EnvironmentOverride(t *testing.T) {
	tests := []struct {
		name string
		env  string
		left int
	}{
		{"zero keeps every thread", "0", 5},
		{"a valid window applies", "5", 1},
		{"garbage is ignored: the stored 30 days apply", "soon", 2},
		{"a negative value is ignored: the stored 30 days apply", "-3", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SP_SUPPORT_RETENTION_DAYS", tt.env)

			r := require.New(t)
			inbox := newSupportInbox(t)
			r.NoError(inbox.db.SetSystemParameter(t.Context(),
				string(systemconfig.KeySupportRetentionDays), 30, false))

			r.NoError((&SupportCleanupJobRun{}).Run(t.Context(), inbox.jctx))
			r.Equal(tt.left, inbox.threads(t))
		})
	}
}

func TestSupportRetentionFromParam_Integers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		value  any
		want   int
		wantOK bool
	}{
		{"int64", int64(45), 45, true},
		{"int64 zero keeps forever", int64(0), 0, true},
		{"negative int64", int64(-1), 0, false},
		{"negative int", -1, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			days, ok := supportRetentionFromParam(models.JSONMap{"value": tt.value})
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, days)
		})
	}
}
