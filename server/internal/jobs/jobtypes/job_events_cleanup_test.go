package jobtypes

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

// newEventsCleanupTestContext wires an in-memory SQLite DB, a real job service
// and a logger, so the sweeper exercises its full path (retention resolution,
// batched delete, self-reschedule).
func newEventsCleanupTestContext(t *testing.T, retentionDays int) (*jobdef.JobContext, db.Service, string) {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	jobSvc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

	jctx := &jobdef.JobContext{
		Services:  &services.Registry{Jobs: jobSvc},
		DB:        dbSvc.DB(),
		DBService: dbSvc,
		Logger:    slog.Default(),
		AppConfig: &config.Config{Audit: config.AuditConfig{RetentionDays: retentionDays}},
	}

	return jctx, dbSvc, org.UID
}

func seedEventAt(t *testing.T, dbSvc db.Service, orgUID string, at time.Time) string {
	t.Helper()

	event := models.NewEvent(orgUID, models.EventTypeCheckCreated, models.ActorTypeSystem)
	event.CreatedAt = at
	require.NoError(t, dbSvc.CreateEvent(t.Context(), event))

	return event.UID
}

func eventUIDs(t *testing.T, dbSvc db.Service, orgUID string) map[string]bool {
	t.Helper()

	rows, err := dbSvc.ListEvents(t.Context(), &models.ListEventsFilter{
		OrganizationUID: orgUID,
		Limit:           1000,
	})
	require.NoError(t, err)

	out := map[string]bool{}
	for _, row := range rows {
		out[row.UID] = true
	}

	return out
}

// TestEventsCleanupDeletesOnlyExpiredEvents. The positive control is the
// "recent" half: a sweep that emptied the table would satisfy "the old rows are
// gone" just as well, and an audit trail that deletes itself is a worse bug
// than one that grows.
func TestEventsCleanupDeletesOnlyExpiredEvents(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jctx, dbSvc, orgUID := newEventsCleanupTestContext(t, 365)

	now := time.Now()
	ancient := seedEventAt(t, dbSvc, orgUID, now.AddDate(0, 0, -400))
	justOver := seedEventAt(t, dbSvc, orgUID, now.AddDate(0, 0, -366))
	justUnder := seedEventAt(t, dbSvc, orgUID, now.AddDate(0, 0, -364))
	recent := seedEventAt(t, dbSvc, orgUID, now.Add(-time.Hour))

	run := &EventsCleanupJobRun{}
	r.NoError(run.Run(t.Context(), jctx))

	remaining := eventUIDs(t, dbSvc, orgUID)
	r.False(remaining[ancient], "a 400-day-old event must be swept")
	r.False(remaining[justOver], "an event one day past the window must be swept")
	r.True(remaining[justUnder], "an event one day inside the window must survive")
	r.True(remaining[recent], "a fresh event must survive")
}

// TestEventsCleanupRetentionZeroKeepsEverything. Retention 0 is an explicit,
// supported "keep forever" for an operator under a legal hold — it must not be
// silently normalized to the 365-day default.
func TestEventsCleanupRetentionZeroKeepsEverything(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jctx, dbSvc, orgUID := newEventsCleanupTestContext(t, -1)

	ancient := seedEventAt(t, dbSvc, orgUID, time.Now().AddDate(-5, 0, 0))

	run := &EventsCleanupJobRun{}
	r.NoError(run.Run(t.Context(), jctx))

	r.True(eventUIDs(t, dbSvc, orgUID)[ancient], "retention disabled must keep even a five-year-old event")
}

// TestEventsCleanupDrainsAcrossBatches proves the loop keeps going past one
// batch. With a batch size of 2 and seven expired rows, a single-shot delete
// would leave five behind.
func TestEventsCleanupDrainsAcrossBatches(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jctx, dbSvc, orgUID := newEventsCleanupTestContext(t, 30)

	now := time.Now()
	for i := 0; i < 7; i++ {
		seedEventAt(t, dbSvc, orgUID, now.AddDate(0, 0, -60-i))
	}

	survivor := seedEventAt(t, dbSvc, orgUID, now.Add(-time.Hour))

	run := &EventsCleanupJobRun{batchSize: 2}
	r.NoError(run.Run(t.Context(), jctx))

	remaining := eventUIDs(t, dbSvc, orgUID)
	r.Len(remaining, 1)
	r.True(remaining[survivor])
}

// TestEventsCleanupBoundsOneRun. A first sweep against years of history must
// not monopolize a job worker: the per-run batch ceiling stops it and the rest
// drains tomorrow.
func TestEventsCleanupBoundsOneRun(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jctx, dbSvc, orgUID := newEventsCleanupTestContext(t, 30)

	now := time.Now()
	for i := 0; i < 10; i++ {
		seedEventAt(t, dbSvc, orgUID, now.AddDate(0, 0, -60-i))
	}

	run := &EventsCleanupJobRun{batchSize: 2, maxBatches: 2}
	r.NoError(run.Run(t.Context(), jctx))

	r.Len(eventUIDs(t, dbSvc, orgUID), 6, "2 batches of 2 must remove exactly 4 of the 10 expired rows")
}

// TestEventsCleanupReschedulesItself — the job is provisioned once at startup
// and keeps itself alive from then on, so a run that forgets to reschedule
// means retention silently stops after the first day.
func TestEventsCleanupReschedulesItself(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	jctx, dbSvc, _ := newEventsCleanupTestContext(t, 365)

	run := &EventsCleanupJobRun{}
	r.NoError(run.Run(t.Context(), jctx))

	count, err := dbSvc.DB().NewSelect().
		Model((*models.Job)(nil)).
		Where("type = ?", string(jobdef.JobTypeEventsCleanup)).
		Count(t.Context())
	r.NoError(err)
	r.Positive(count, "the sweeper must enqueue its own next run")
}

func TestResolveAuditRetentionDays(t *testing.T) {
	t.Parallel()

	envOf := func(v string, set bool) func(string) (string, bool) {
		return func(string) (string, bool) { return v, set }
	}
	noEnv := envOf("", false)

	tests := []struct {
		name   string
		env    func(string) (string, bool)
		param  any
		koanf  int
		noCfg  bool
		expect int
	}{
		{name: "env zero", env: envOf("0", true), koanf: 30, expect: 0},
		{name: "env negative", env: envOf("-5", true), koanf: 30, expect: 0},
		{name: "env 30", env: envOf("30", true), koanf: 365, expect: 30},
		{name: "env garbage falls through", env: envOf("abc", true), koanf: 90, expect: 90},
		{name: "env empty falls through", env: envOf("", true), koanf: 90, expect: 90},
		{name: "param zero", env: noEnv, param: float64(0), koanf: 30, expect: 0},
		{name: "param 30", env: noEnv, param: float64(30), koanf: 365, expect: 30},
		{name: "koanf zero", env: noEnv, koanf: 0, expect: 0},
		{name: "koanf 30", env: noEnv, koanf: 30, expect: 30},
		{name: "nothing set", env: noEnv, noCfg: true, expect: 365},
		{name: "env zero beats param 30", env: envOf("0", true), param: float64(30), koanf: 30, expect: 0},
		{name: "param zero beats koanf 30", env: noEnv, param: float64(0), koanf: 30, expect: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			jctx, dbSvc, _ := newEventsCleanupTestContext(t, tc.koanf)
			if tc.noCfg {
				jctx.AppConfig = nil
			}

			if tc.param != nil {
				require.NoError(t, dbSvc.SetSystemParameter(t.Context(), string(systemconfig.KeyAuditRetentionDays),
					tc.param, false))
			}

			require.Equal(t, tc.expect, resolveAuditRetentionDays(context.Background(), jctx, tc.env))
		})
	}
}

// Positive control: retention 30 deletes the old event that retention 0 keeps.
func TestEventsCleanupRetentionZeroVsPositive(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		days int
		kept bool
	}{{0, true}, {30, false}} {
		r := require.New(t)
		jctx, dbSvc, orgUID := newEventsCleanupTestContext(t, tc.days)
		old := seedEventAt(t, dbSvc, orgUID, time.Now().AddDate(0, 0, -400))

		r.NoError((&EventsCleanupJobRun{}).Run(t.Context(), jctx))
		r.Equal(tc.kept, eventUIDs(t, dbSvc, orgUID)[old], "retention %d", tc.days)
	}
}

// 0 stays invalid for the aggregation tiers.
func TestResolveRetentionTierEnvZeroStillFallsThrough(t *testing.T) {
	t.Setenv("SP_TEST_RETENTION_TIER_ZERO", "0")

	got := resolveRetentionTier(t.Context(), nil, systemconfig.KeyAuditRetentionDays,
		"SP_TEST_RETENTION_TIER_ZERO", 0, 77)
	require.Equal(t, 77, got)
}
