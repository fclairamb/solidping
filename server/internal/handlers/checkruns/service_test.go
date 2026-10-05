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
	svc := checkruns.NewService(dbSvc, store)

	org := models.NewOrganization("runs", "Runs")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "crawl-acme", "crawl")
	check.Config = models.JSONMap{"url": "https://www.acme.com/"}
	r.NoError(dbSvc.CreateCheck(ctx, check))

	// No run in progress.
	run, err := svc.GetRun(ctx, org.Slug, *check.Slug)
	r.NoError(err)
	r.False(run.Running)

	// A run in progress, with progress in its state file's details.
	envelope, err := attachments.EncodeStepState("run-1", "crawl", []byte(`{"queue":[]}`))
	r.NoError(err)

	fileUID, err := store.PutStepState(ctx, org.UID, check.UID, envelope,
		attachments.StepStateDetails("run-1", 3, map[string]any{"pagesDone": 42, "maxPages": 200}))
	r.NoError(err)

	started := time.Now().Add(-time.Minute)
	_, err = dbSvc.DB().NewUpdate().Model((*models.CheckJob)(nil)).
		Set("step_run_uid = ?", "run-1").
		Set("step_run_started_at = ?", started).
		Set("step_count = ?", 3).
		Set("step_state_file_uid = ?", fileUID).
		Where("check_uid = ?", check.UID).Exec(ctx)
	r.NoError(err)

	run, err = svc.GetRun(ctx, org.Slug, check.UID)
	r.NoError(err)
	r.True(run.Running)
	r.Equal("run-1", run.RunUID)
	r.Equal(3, run.Steps)
	r.InDelta(42, run.Progress["pagesDone"], 0)

	// Cancel: columns cleared, state purged, next run scheduled.
	r.NoError(svc.CancelRun(ctx, org.Slug, check.UID))

	run, err = svc.GetRun(ctx, org.Slug, check.UID)
	r.NoError(err)
	r.False(run.Running)

	stateTopic := models.ListFilesFilter{Topic: attachments.CheckStepStateTopic(check.UID)}
	states, _, err := dbSvc.ListFiles(ctx, org.UID, stateTopic)
	r.NoError(err)
	r.Empty(states)

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
