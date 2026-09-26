package incidents_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
)

// realAttachmentStore wires the REAL attachment service (local FS in a temp
// dir) onto the setup's database, so a test reads back what was stored rather
// than what a fake recorded.
func realAttachmentStore(t *testing.T, s *failureSnapshotSetup) *attachments.Service {
	t.Helper()

	localfs.Register()

	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "test-secret"
	cfg.FileStorage.Type = "local"
	cfg.FileStorage.LocalRoot = t.TempDir()

	store := attachments.NewService(files.NewService(s.dbSvc, cfg), s.dbSvc, cfg)
	s.svc.SetAttachmentStore(store)

	return store
}

// TestValidatingRunKeepsOneCheckScopedCapture is spec 2026-09-25-34 part 2, end
// to end on a real store: a `down` run inside the confirmation period opens no
// incident, and its capture is kept under the check — exactly one, with the
// check-failure trigger and the region taken from the result row.
func TestValidatingRunKeepsOneCheckScopedCapture(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)
	store := realAttachmentStore(t, s)

	s.check.ConfirmationPeriodSeconds = 3600

	r.NoError(s.svc.ProcessCheckResult(ctx, s.check, shotDownResult(s.org.UID, s.check.UID, "blip")))

	active, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.Error(err, "the run is inside the confirmation period: no incident")
	r.Nil(active)

	list, err := store.ListCheckScreenshots(ctx, s.org.UID, s.check.UID, 10)
	r.NoError(err)
	r.Len(list, 1, "exactly one check-scoped capture")
	r.Empty(list[0].IncidentUID)
	r.Equal(attachments.TriggerCheckFailure, list[0].Trigger)
	r.Equal("eu", list[0].Region)

	// Positive control for "only captures no incident took": the run that
	// opens the incident puts its capture on the incident, not on the check.
	s.check.ConfirmationPeriodSeconds = 0
	r.NoError(s.svc.ProcessCheckResult(ctx, s.check, shotDownResult(s.org.UID, s.check.UID, "onset")))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)

	list, err = store.ListCheckScreenshots(ctx, s.org.UID, s.check.UID, 10)
	r.NoError(err)
	r.Len(list, 2)
	r.Equal(inc.UID, list[0].IncidentUID, "the newest is the incident's onset capture")
	r.Equal(attachments.TriggerIncidentOpen, list[0].Trigger)
	r.Empty(list[1].IncidentUID)
}

// TestOnDemandCaptureOfHealthyRunIsKept pins "Capture now" on the server side:
// an `up` result carrying an on-demand capture is stored under the check with
// the capture-now trigger. The same result WITHOUT the on-demand stamp is the
// control — a capture on a healthy run nobody asked for is refused.
func TestOnDemandCaptureOfHealthyRunIsKept(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	store := &fakeAttachmentStore{}
	s.svc.SetAttachmentStore(store)

	upWithShot := func(onDemand bool) *models.Result {
		result := models.NewResult(s.org.UID, s.check.UID, models.ResultStatusUp, 0)
		region := "eu"
		result.Region = &region
		result.Diagnostics = &checkerdef.Diagnostics{Screenshot: &checkerdef.Screenshot{
			Image:      screenshotImage("healthy"),
			Format:     checkerdef.ImageFormatWebP,
			CapturedAt: time.Now().UTC(),
			OnDemand:   onDemand,
		}}

		return result
	}

	r.NoError(s.svc.ProcessCheckResult(ctx, s.check, upWithShot(false)))
	r.Empty(store.checkSnapshot(), "an unrequested capture of a healthy run is not stored")

	r.NoError(s.svc.ProcessCheckResult(ctx, s.check, upWithShot(true)))

	puts := store.checkSnapshot()
	r.Len(puts, 1)
	r.Equal(s.check.UID, puts[0].checkUID)
	r.Equal(attachments.TriggerCaptureNow, puts[0].details[attachments.DetailKeyTrigger])
	r.Equal(s.check.UID, puts[0].details[attachments.DetailKeyCheckUID])
	r.Equal("eu", puts[0].details[attachments.DetailKeyRegion])

	incidentPuts, _ := store.snapshot()
	r.Empty(incidentPuts, "a healthy run writes no incident evidence")
}

// TestOnDemandAgentMarkerAsksForTheCheckTopic is the agent half of "Capture
// now": an on-demand marker on a healthy run asks the agent that produced it
// for the bytes under the CHECK's topic, built server-side.
func TestOnDemandAgentMarkerAsksForTheCheckTopic(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	requester := &fakeUploadRequester{}
	s.svc.SetAgentUploadRequester(requester)

	workerUID := testWorkerUID
	result := models.NewResult(s.org.UID, s.check.UID, models.ResultStatusUp, 0)
	result.WorkerUID = &workerUID
	result.Diagnostics = &checkerdef.Diagnostics{Screenshot: &checkerdef.Screenshot{
		Available: true, CaptureID: "cap-now", OnDemand: true,
	}}

	r.NoError(s.svc.ProcessCheckResult(ctx, s.check, result))

	asks := requester.snapshot()
	r.Len(asks, 1)
	r.Equal(testWorkerUID, asks[0].workerUID)
	r.Equal("cap-now", asks[0].captureID)
	r.Equal(attachments.CheckScreenshotTopic(s.check.UID), asks[0].topic)
}
