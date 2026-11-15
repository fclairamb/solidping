package checkworker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// TestRestartedWorkerReclaimsItsOwnLeases covers spec 2026-11-14-03: a worker
// that comes back under the slug (so the UID) of a crashed process frees the
// leases that process left and runs those jobs on its first fetch, instead of
// waiting for the lease to expire an hour later.
//
//nolint:paralleltest // registers a Prometheus collector, like the other Run tests
func TestRestartedWorkerReclaimsItsOwnLeases(t *testing.T) {
	ctx := context.Background()
	r := require.New(t)

	svc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	t.Cleanup(func() { _ = svc.Close() })
	r.NoError(svc.Initialize(ctx))

	const slug = "restart-node"

	cfg := &config.Config{
		Node: config.NodeConfig{Name: slug},
		Server: config.ServerConfig{
			CheckWorker: config.CheckWorkerConfig{Nb: 2, FetchMaxAhead: 5 * time.Minute},
		},
	}

	svcList := services.NewRegistry()
	checkJobSvc := checkjobsvc.NewService(svc.DB())
	svcList.CheckJobs = checkJobSvc
	eventNotifier := notifier.NewLocalEventNotifier()
	t.Cleanup(func() { _ = eventNotifier.Close() })
	svcList.EventNotifier = eventNotifier

	// The previous process: same slug, so registration hands back this UID.
	previous, err := svc.RegisterOrUpdateWorker(ctx, models.NewWorker(slug, slug))
	r.NoError(err)

	org := models.NewOrganization("restart-org", "")
	r.NoError(svc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "restart-sleep", string(checkerdef.CheckTypeSleep))
	check.Config = models.JSONMap{"sleep_ms": 1}
	r.NoError(svc.CreateCheck(ctx, check))

	// Leased by the previous process a minute ago, valid for another hour.
	claimedAt := time.Now().Add(-time.Minute)
	due := claimedAt.Add(-time.Second)
	_, err = svc.DB().NewUpdate().Model((*models.CheckJob)(nil)).
		Set("scheduled_at = ?", due).
		Set("effective_scheduled_at = ?", due).
		Set("lease_worker_uid = ?", previous.UID).
		Set("lease_expires_at = ?", time.Now().Add(time.Hour)).
		Set("lease_starts = 1").
		Set("updated_at = ?", claimedAt).
		Where("check_uid = ?", check.UID).
		Exec(ctx)
	r.NoError(err)

	runner := NewCheckWorker(svc, cfg, svcList, checkJobSvc)

	runCtx, cancel := context.WithCancel(ctx)
	runDone := make(chan error, 1)

	go func() { runDone <- runner.Run(runCtx) }()

	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone: //nolint:errcheck // exit error irrelevant here
		case <-time.After(10 * time.Second):
			t.Error("worker did not shut down")
		}
	})

	r.Eventually(func() bool {
		var n int
		if scanErr := svc.DB().NewSelect().Model((*models.Result)(nil)).
			ColumnExpr("count(*)").
			Where("check_uid = ?", check.UID).
			Where("status != ?", int(models.ResultStatusCreated)).
			Scan(ctx, &n); scanErr != nil {
			return false
		}

		return n > 0
	}, 10*time.Second, 20*time.Millisecond, "the restarted worker must run the job its predecessor leased")

	worker := runner.getWorker()
	r.NotNil(worker)
	r.Equal(previous.UID, worker.UID, "same slug, same worker UID")
}
