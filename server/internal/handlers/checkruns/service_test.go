package checkruns_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/handlers/checkruns"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
	"github.com/fclairamb/solidping/server/internal/workertasks"
)

func TestCheckRunEndpoints(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	localfs.Register()

	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "test-secret"
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = t.TempDir()

	store := attachments.NewService(files.NewService(dbSvc, cfg), dbSvc, cfg)
	tasks := workertasks.NewService(dbSvc.DB())
	tasks.SetStateStore(store)
	svc := checkruns.NewService(dbSvc, store, tasks)

	org := models.NewOrganization("runs", "Runs")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "crawl-acme", "crawl")
	check.Config = models.JSONMap{"url": "https://www.acme.com/"}
	r.NoError(dbSvc.CreateCheck(ctx, check))

	// No run in progress.
	run, err := svc.GetRun(ctx, org.Slug, *check.Slug)
	r.NoError(err)
	r.False(run.Running)

	// A run in progress is the check's active worker task (spec 2026-10-22-03).
	task := models.NewWorkerTask(org.UID, "crawl", "eu-west")
	task.CheckUID = &check.UID
	r.NoError(tasks.Create(ctx, task))

	started := time.Now().Add(-time.Minute).UTC()
	_, err = dbSvc.DB().NewUpdate().Model((*models.WorkerTask)(nil)).
		Set("run_started_at = ?", started).
		Set("slice_count = ?", 3).
		Set("progress = ?", models.JSONMap{"pagesDone": 42, "maxPages": 200}).
		Where("uid = ?", task.UID).Exec(ctx)
	r.NoError(err)

	run, err = svc.GetRun(ctx, org.Slug, check.UID)
	r.NoError(err)
	r.True(run.Running)
	r.Equal(task.UID, run.RunUID)
	r.Equal(3, run.Steps)
	r.WithinDuration(started, *run.StartedAt, time.Second)
	r.InDelta(42, run.Progress["pagesDone"], 0)

	// Cancel: the task is canceled, no run in progress any more.
	r.NoError(svc.CancelRun(ctx, org.Slug, check.UID))

	run, err = svc.GetRun(ctx, org.Slug, check.UID)
	r.NoError(err)
	r.False(run.Running)

	canceled, err := tasks.Get(ctx, task.UID)
	r.NoError(err)
	r.Equal(models.WorkerTaskStatusCancelled, canceled.Status)

	// Reports.
	_, err = store.PutCrawlReport(ctx, org.UID, check.UID, []byte(`{"findings":[]}`), nil)
	r.NoError(err)

	reports, err := svc.ListCrawlReports(ctx, org.Slug, check.UID)
	r.NoError(err)
	r.Len(reports, 1)
	r.NotEmpty(reports[0].DownloadURL)

	// Errors.
	_, err = svc.GetRun(ctx, "nope", check.UID)
	r.ErrorIs(err, checkruns.ErrOrganizationNotFound)
	_, err = svc.GetRun(ctx, org.Slug, "nope")
	r.ErrorIs(err, checkruns.ErrCheckNotFound)

	httpCheck := models.NewCheck(org.UID, "web", "http")
	httpCheck.Config = models.JSONMap{"url": "https://www.acme.com/"}
	r.NoError(dbSvc.CreateCheck(ctx, httpCheck))
	r.ErrorIs(svc.CancelRun(ctx, org.Slug, httpCheck.UID), checkruns.ErrNotMultiStep)
}
