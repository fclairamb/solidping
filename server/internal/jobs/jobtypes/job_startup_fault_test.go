package jobtypes

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/testsupport"
)

// TestDemoProvisioningSurvivesEveryQueryFailure provisions the demo account (org,
// user, entitlements, sink policy, catalog, status page, SLOs, maintenance,
// backfill) on a fresh database once per query it issues, failing a different query each
// time. A failure must come back as an error or be logged as non-fatal,
// never panic, and the clean run must succeed.
func TestDemoProvisioningSurvivesEveryQueryFailure(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("replays the demo provisioning a few hundred times (about a minute)")
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runs, failed := 0, 0

	testsupport.SweepFresh(t, func() (*testsupport.FaultHook, func()) {
		ctx := t.Context()
		dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
		require.NoError(t, err)
		require.NoError(t, dbSvc.Initialize(ctx))
		hook := testsupport.InstallFaults(dbSvc.DB())

		cfg := &config.Config{}
		cfg.Demo = config.DemoConfig{
			Enabled: true, OrgSlug: "demo", Email: "demo@acme.com", Password: "demo",
			CheckTTL: time.Hour, CleanupInterval: 30 * time.Minute,
		}
		cfg.Server.BaseURL = "http://localhost:4000"
		jobSvc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)
		jctx := &jobdef.JobContext{
			Services:  &services.Registry{Jobs: jobSvc, Checks: &fakeCheckDeleter{dbSvc: dbSvc}},
			DB:        dbSvc.DB(),
			DBService: dbSvc,
			Logger:    logger,
			AppConfig: cfg,
		}

		return hook, func() {
			defer func() { _ = dbSvc.Close() }()
			err := (&StartupJobRun{}).ensureDemoOrganization(ctx, jctx)
			if runs == 0 {
				require.NoError(t, err, "the clean run succeeds")
			}
			runs++
			if err != nil {
				failed++
			}
		}
	})

	require.Greater(t, runs, 20)
	_ = failed // demo provisioning logs most failures as non-fatal
}
