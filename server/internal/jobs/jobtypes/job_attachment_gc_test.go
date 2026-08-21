package jobtypes

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// attachmentGCSetup is a database with one org, ready for attachment rows.
func attachmentGCSetup(t *testing.T) (context.Context, db.Service, *models.Organization) {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("acme", "Acme")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	return ctx, dbSvc, org
}

// writeAttachment inserts an attachment row directly (no blob), aged by `age`.
func writeAttachment(ctx context.Context, t *testing.T, dbSvc db.Service,
	orgUID, topic string, age time.Duration,
) *models.File {
	t.Helper()

	file := models.NewFile(orgUID, "shot.png", "image/png", "file://blob", 42, nil)
	file.Topic = &topic
	file.CreatedAt = time.Now().Add(-age)

	require.NoError(t, dbSvc.CreateFile(ctx, file))

	return file
}

// TestSweepOrphanIncidentAttachments is the GC's contract in one test: an
// attachment whose incident is gone is reaped, and NOTHING else is.
//
// The four survivors are the whole point — a sweep that deleted everything
// would satisfy the first assertion just as well.
func TestSweepOrphanIncidentAttachments(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, dbSvc, org := attachmentGCSetup(t)

	check := models.NewCheck(org.UID, "api", "browser")
	r.NoError(dbSvc.CreateCheck(ctx, check))

	live := models.NewIncident(org.UID, check.UID, time.Now(), "api is down")
	r.NoError(dbSvc.CreateIncident(ctx, live))

	oldEnough := attachmentOrphanGrace + time.Hour

	orphan := writeAttachment(ctx, t, dbSvc, org.UID,
		attachments.IncidentScreenshotTopic(uuid.New().String()), oldEnough)
	attached := writeAttachment(ctx, t, dbSvc, org.UID,
		attachments.IncidentScreenshotTopic(live.UID), oldEnough)
	fresh := writeAttachment(ctx, t, dbSvc, org.UID,
		attachments.IncidentScreenshotTopic(uuid.New().String()), time.Minute)
	malformed := writeAttachment(ctx, t, dbSvc, org.UID, "incidents/only-two-parts", oldEnough)

	// A plain file with no topic at all: the ordinary case the sweep must be
	// blind to.
	plain := models.NewFile(org.UID, "logo.png", "image/png", "file://logo", 3, nil)
	plain.CreatedAt = time.Now().Add(-oldEnough)
	r.NoError(dbSvc.CreateFile(ctx, plain))

	sweepOrphanIncidentAttachments(ctx, &jobdef.JobContext{
		DBService: dbSvc,
		Logger:    slog.Default(),
	})

	_, err := dbSvc.GetFile(ctx, org.UID, orphan.UID)
	r.Error(err, "an attachment whose incident is gone must be reaped")

	for name, file := range map[string]*models.File{
		"attached to a live incident": attached,
		"inside the grace window":     fresh,
		"malformed topic":             malformed,
		"not an attachment":           plain,
	} {
		_, err := dbSvc.GetFile(ctx, org.UID, file.UID)
		r.NoError(err, "%s must survive the sweep", name)
	}
}

// TestListAttachmentsByTopicPrefixIgnoresNonAttachments pins the query the
// sweep is built on: it must see attachment rows only, and only those older
// than the cutoff.
func TestListAttachmentsByTopicPrefixIgnoresNonAttachments(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, dbSvc, org := attachmentGCSetup(t)

	old := writeAttachment(ctx, t, dbSvc, org.UID,
		attachments.IncidentScreenshotTopic(uuid.New().String()), 48*time.Hour)
	writeAttachment(ctx, t, dbSvc, org.UID,
		attachments.IncidentScreenshotTopic(uuid.New().String()), time.Minute)

	plain := models.NewFile(org.UID, "logo.png", "image/png", "file://logo", 3, nil)
	plain.CreatedAt = time.Now().Add(-48 * time.Hour)
	r.NoError(dbSvc.CreateFile(ctx, plain))

	rows, err := dbSvc.ListAttachmentsByTopicPrefix(ctx,
		attachments.EntityIncidents+"/", time.Now().Add(-time.Hour), 100)
	r.NoError(err)
	r.Len(rows, 1)
	r.Equal(old.UID, rows[0].UID)
}
