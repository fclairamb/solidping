package jobtypes

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// TestSweepExpiredReportFiles is the spec 2026-10-29-01 retention test: a
// `reports` file older than the retention is purged with its blob, a younger
// one is kept, and an equally old file of another group is not touched.
func TestSweepExpiredReportFiles(t *testing.T) {
	t.Parallel()

	localfs.Register()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme-retention", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	cfg := &config.Config{}
	cfg.App.FeedbackRetentionDays = 90
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = t.TempDir()

	filesSvc := files.NewService(dbSvc, cfg)
	orgUUID := uuid.MustParse(org.UID)

	create := func(group filestorage.GroupType, name string, age time.Duration) *models.File {
		file, createErr := filesSvc.CreateFile(ctx, orgUUID, group, name, "image/png", nil,
			strings.NewReader("png"), 3)
		r.NoError(createErr)

		_, updErr := dbSvc.DB().NewUpdate().Model((*models.File)(nil)).
			Set("created_at = ?", time.Now().Add(-age)).
			Where("uid = ?", file.UID).
			Exec(ctx)
		r.NoError(updErr)

		return file
	}

	old := create(filestorage.GroupTypeReports, "old.png", 91*24*time.Hour)
	young := create(filestorage.GroupTypeReports, "young.png", 89*24*time.Hour)
	otherGroup := create(filestorage.GroupTypeScreenshots, "other.png", 200*24*time.Hour)

	jctx := &jobdef.JobContext{DBService: dbSvc, AppConfig: cfg, Logger: slog.Default()}
	sweepExpiredReportFiles(ctx, jctx)

	_, err = dbSvc.GetFile(ctx, org.UID, old.UID)
	r.Error(err, "a reports file past the retention must be deleted")

	_, err = filesSvc.OpenContent(ctx, old)
	r.Error(err, "its blob must be removed from storage too")

	_, err = dbSvc.GetFile(ctx, org.UID, young.UID)
	r.NoError(err, "a reports file younger than the retention must be kept")

	body, err := filesSvc.OpenContent(ctx, young)
	r.NoError(err, "the kept file's blob must still be there")
	r.NoError(body.Close())

	_, err = dbSvc.GetFile(ctx, org.UID, otherGroup.UID)
	r.NoError(err, "files of other groups are not under the report retention")

	// Retention 0 keeps report files forever.
	cfg.App.FeedbackRetentionDays = 0
	ancient := create(filestorage.GroupTypeReports, "ancient.png", 1000*24*time.Hour)
	sweepExpiredReportFiles(ctx, jctx)

	_, err = dbSvc.GetFile(ctx, org.UID, ancient.UID)
	r.NoError(err, "a retention of 0 must keep report files")
}

// TestSweepExpiredReportFiles_NoConfig: without an app config the sweep is a
// no-op rather than a nil dereference.
func TestSweepExpiredReportFiles_NoConfig(t *testing.T) {
	t.Parallel()

	require.NotPanics(t, func() {
		sweepExpiredReportFiles(t.Context(), &jobdef.JobContext{Logger: slog.Default()})
	})
}

// TestSweepExpiredReportFiles_ListError: a failing listing is logged and the
// sweep returns without purging anything.
func TestSweepExpiredReportFiles_ListError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(t.Context()))
	r.NoError(dbSvc.Close())

	cfg := &config.Config{}
	cfg.App.FeedbackRetentionDays = 90

	require.NotPanics(t, func() {
		sweepExpiredReportFiles(t.Context(), &jobdef.JobContext{
			DBService: dbSvc, AppConfig: cfg, Logger: slog.Default(),
		})
	})
}

// TestSweepExpiredReportFiles_SparesTopicFiles: a website crawl report (same
// `reports` group, but attached under a topic) older than the retention
// survives the sweep with its blob, while a topic-less file is purged.
func TestSweepExpiredReportFiles_SparesTopicFiles(t *testing.T) {
	t.Parallel()

	localfs.Register()

	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme-spare", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	cfg := &config.Config{}
	cfg.App.FeedbackRetentionDays = 90
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = t.TempDir()

	filesSvc := files.NewService(dbSvc, cfg)
	orgUUID := uuid.MustParse(org.UID)
	topic := "checks/abc/crawl-report"

	create := func(name string, opts ...files.CreateFileOption) *models.File {
		file, createErr := filesSvc.CreateFile(ctx, orgUUID, filestorage.GroupTypeReports, name,
			"text/html", nil, strings.NewReader("x"), 1, opts...)
		r.NoError(createErr)

		_, updErr := dbSvc.DB().NewUpdate().Model((*models.File)(nil)).
			Set("created_at = ?", time.Now().Add(-200*24*time.Hour)).
			Where("uid = ?", file.UID).
			Exec(ctx)
		r.NoError(updErr)

		return file
	}

	crawl := create("crawl.html", files.WithTopic(topic))
	shot := create("shot.png")

	sweepExpiredReportFiles(ctx, &jobdef.JobContext{DBService: dbSvc, AppConfig: cfg, Logger: slog.Default()})

	_, err = dbSvc.GetFile(ctx, org.UID, crawl.UID)
	r.NoError(err, "a crawl report must survive the screenshot retention")

	body, err := filesSvc.OpenContent(ctx, crawl)
	r.NoError(err, "the crawl report blob must still be there")
	r.NoError(body.Close())

	_, err = dbSvc.GetFile(ctx, org.UID, shot.UID)
	r.Error(err, "the topic-less screenshot is still purged")
}
