package jobtypes_test

import (
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// filesGCFixture wires an in-memory database, a real job service and a
// temp-dir storage backend, so the sweep exercises its whole path — including
// the byte-level delete, which is the half a mocked backend would hide.
type filesGCFixture struct {
	jctx  *jobdef.JobContext
	dbSvc *sqlite.Service
	root  string
	org   *models.Organization
	check *models.Check
}

func newFilesGCFixture(t *testing.T) *filesGCFixture {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	localfs.Register()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "shop", "browser")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	root := t.TempDir()
	cfg := &config.Config{}
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = root

	jobSvc := jobsvc.NewService(dbSvc.DB(), dbSvc, notifier.NewLocalEventNotifier(), nil)

	return &filesGCFixture{
		jctx: &jobdef.JobContext{
			Services:  &services.Registry{Jobs: jobSvc},
			DB:        dbSvc.DB(),
			DBService: dbSvc,
			AppConfig: cfg,
			Logger:    slog.Default(),
		},
		dbSvc: dbSvc,
		root:  root,
		org:   org,
		check: check,
	}
}

// writeAttachment writes real bytes through the storage backend and the
// matching `files` row, so the sweep has something genuine to delete.
func (f *filesGCFixture) writeAttachment(t *testing.T, name, topic string) *models.File {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	orgUID, err := uuid.Parse(f.org.UID)
	r.NoError(err)

	storage, err := filestorage.NewFileStorage(&filestorage.Config{
		Type: "local", LocalRoot: f.root,
	})
	r.NoError(err)

	fileID := uuid.New().String()

	uri, err := storage.WriteFile(
		ctx, orgUID, filestorage.GroupTypeScreenshots, fileID,
		strings.NewReader("pixels"), filestorage.FileMetadata{MimeType: "image/png", Size: 6},
	)
	r.NoError(err)

	file := models.NewFile(f.org.UID, name, "image/png", uri, 6, nil)
	if topic != "" {
		file.Topic = &topic
	}

	r.NoError(f.dbSvc.CreateFile(ctx, file))

	return file
}

// blobPath is where the local backend put a file's bytes.
func (f *filesGCFixture) blobPath(t *testing.T, file *models.File) string {
	t.Helper()

	r := require.New(t)

	storage, err := filestorage.GetStorageForURI(file.FileURI, &filestorage.Config{
		Type: "local", LocalRoot: f.root,
	})
	r.NoError(err)

	orgUID, group, fileID, err := storage.ParseURI(file.FileURI)
	r.NoError(err)

	return filepath.Join(f.root, filestorage.BuildPath(orgUID, group, fileID))
}

// age backdates a file's created_at so it clears the sweep's grace window.
func (f *filesGCFixture) age(t *testing.T, file *models.File, by time.Duration) {
	t.Helper()

	_, err := f.dbSvc.DB().ExecContext(t.Context(),
		`update files set created_at = ? where uid = ?`,
		time.Now().Add(-by).Format("2006-01-02 15:04:05"), file.UID)
	require.NoError(t, err)
}

// TestFilesGCSweepsOrphanAttachments is the reaper the attachments rail needs:
// an attachment whose incident is gone is soft-deleted; one whose incident is
// alive, and a plain non-attachment file, are both left alone.
func TestFilesGCSweepsOrphanAttachments(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	f := newFilesGCFixture(t)

	live := models.NewIncident(f.org.UID, f.check.UID, time.Now(), "live")
	r.NoError(f.dbSvc.CreateIncident(ctx, live))

	topicFor := func(incidentUID string) string {
		return models.AttachmentTopic(
			models.AttachmentEntityIncidents, incidentUID, models.AttachmentKindScreenshot,
		)
	}

	kept := f.writeAttachment(t, "live.png", topicFor(live.UID))
	orphan := f.writeAttachment(t, "orphan.png", topicFor(uuid.New().String()))
	plain := f.writeAttachment(t, "logo.png", "")

	// Everything is old enough to clear the grace window.
	for _, file := range []*models.File{kept, orphan, plain} {
		f.age(t, file, 6*time.Hour)
	}

	run, err := (&jobtypes.FilesGCJobDefinition{}).CreateJobRun(nil)
	r.NoError(err)
	r.NoError(run.Run(ctx, f.jctx))

	// The orphan's ROW is soft-deleted; its bytes stay until the retention
	// window elapses, which is what makes the delete recoverable.
	_, err = f.dbSvc.GetFile(ctx, f.org.UID, orphan.UID)
	r.Error(err, "the orphan is no longer a live file")
	r.FileExists(f.blobPath(t, orphan), "but its bytes survive the retention window")

	for _, file := range []*models.File{kept, plain} {
		_, getErr := f.dbSvc.GetFile(ctx, f.org.UID, file.UID)
		r.NoError(getErr, "%s must be untouched", file.Name)
	}
}

// TestFilesGCRespectsTheOrphanGraceWindow proves the sweep cannot race a write
// that is about to become valid: the two writes (blob, then the entity that
// names it) are not atomic, so a freshly-created orphan is left alone.
func TestFilesGCRespectsTheOrphanGraceWindow(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	f := newFilesGCFixture(t)

	topic := models.AttachmentTopic(
		models.AttachmentEntityIncidents, uuid.New().String(), models.AttachmentKindScreenshot,
	)
	fresh := f.writeAttachment(t, "fresh.png", topic)

	run, err := (&jobtypes.FilesGCJobDefinition{}).CreateJobRun(nil)
	r.NoError(err)
	r.NoError(run.Run(ctx, f.jctx))

	_, err = f.dbSvc.GetFile(ctx, f.org.UID, fresh.UID)
	r.NoError(err, "an orphan inside the grace window is left alone")

	// Positive control: past the window, the same file IS swept — so the
	// assertion above is about the window and not about a sweep that never
	// deletes anything.
	f.age(t, fresh, 6*time.Hour)
	r.NoError(run.Run(ctx, f.jctx))

	_, err = f.dbSvc.GetFile(ctx, f.org.UID, fresh.UID)
	r.Error(err)
}

// TestFilesGCPurgesBytesOfLongDeletedFiles is the second pass: past the
// retention window the bytes go, and only then the row.
func TestFilesGCPurgesBytesOfLongDeletedFiles(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	f := newFilesGCFixture(t)

	old := f.writeAttachment(t, "old.png", "")
	recent := f.writeAttachment(t, "recent.png", "")

	oldPath, recentPath := f.blobPath(t, old), f.blobPath(t, recent)

	// Positive control: both blobs are really on disk before the sweep.
	r.FileExists(oldPath)
	r.FileExists(recentPath)

	r.NoError(f.dbSvc.DeleteFile(ctx, f.org.UID, old.UID))
	r.NoError(f.dbSvc.DeleteFile(ctx, f.org.UID, recent.UID))

	// Backdate only the first one's soft-delete past the retention window.
	_, err := f.dbSvc.DB().ExecContext(ctx,
		`update files set deleted_at = datetime('now', '-30 days') where uid = ?`, old.UID)
	r.NoError(err)

	run, err := (&jobtypes.FilesGCJobDefinition{}).CreateJobRun(nil)
	r.NoError(err)
	r.NoError(run.Run(ctx, f.jctx))

	r.NoFileExists(oldPath, "the long-deleted blob is gone")
	r.FileExists(recentPath, "a recently deleted blob is still recoverable")

	// The row went with the bytes — never the other way round, which would
	// leave a dangling pointer a reader 500s on.
	var count int
	r.NoError(f.dbSvc.DB().
		QueryRowContext(ctx, `select count(*) from files where uid = ?`, old.UID).Scan(&count))
	r.Zero(count)
}

// TestFilesGCReschedulesItself: the sweep is self-perpetuating, so a missed
// reschedule would silently stop all storage hygiene.
func TestFilesGCReschedulesItself(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()
	f := newFilesGCFixture(t)

	run, err := (&jobtypes.FilesGCJobDefinition{}).CreateJobRun(nil)
	r.NoError(err)
	r.NoError(run.Run(ctx, f.jctx))

	var count int
	r.NoError(f.dbSvc.DB().QueryRowContext(ctx,
		`select count(*) from jobs where type = ?`, string(jobdef.JobTypeFilesGC)).Scan(&count))
	r.Equal(1, count, "exactly one follow-up sweep is queued")
}
