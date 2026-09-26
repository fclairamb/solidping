package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
)

// TestDeleteCheckReapsCheckScopedScreenshots pins spec 2026-09-25-34's reaping
// rule: deleting a check soft-deletes everything under `checks/<uid>/`, and
// nothing belonging to another check.
func TestDeleteCheckReapsCheckScopedScreenshots(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	svc, dbSvc, org := newStatsService(t, "delete-reaps-shots")

	doomed, err := svc.CreateCheck(ctx, org.Slug, httpCheckReq())
	r.NoError(err)

	survivor := models.NewCheck(org.UID, "survivor", "browser")
	r.NoError(dbSvc.CreateCheck(ctx, survivor))

	write := func(topic string) *models.File {
		file := models.NewFile(org.UID, "shot.png", "image/png", "file://x", 1, nil)
		file.Topic = &topic
		r.NoError(dbSvc.CreateFile(ctx, file))

		return file
	}

	first := write(attachments.CheckScreenshotTopic(doomed.UID))
	second := write(attachments.CheckScreenshotTopic(doomed.UID))
	kept := write(attachments.CheckScreenshotTopic(survivor.UID))

	r.NoError(svc.DeleteCheck(ctx, org.Slug, doomed.UID))

	for _, file := range []*models.File{first, second} {
		_, getErr := dbSvc.GetFile(ctx, org.UID, file.UID)
		r.Error(getErr, "the deleted check's captures are reaped")
	}

	_, err = dbSvc.GetFile(ctx, org.UID, kept.UID)
	r.NoError(err, "another check's capture survives")
}
