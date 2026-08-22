package incidents_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/attachments"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// fakeAttachmentStore records every call the incident pipeline makes, so a
// test can assert on WHAT was written and WHEN — including the negative case
// (nothing written at all), which a real store would make hard to see.
type fakeAttachmentStore struct {
	mu sync.Mutex

	created []createdAttachment
	deleted []string
	views   []attachments.View

	// createErr / deleteErr force the best-effort paths.
	createErr error
	deleteErr error
}

type createdAttachment struct {
	orgUID   string
	name     string
	mimeType string
	topic    string
	details  models.JSONMap
	body     []byte
}

func (f *fakeAttachmentStore) CreateAttachment(
	_ context.Context, orgUID uuid.UUID, name, mimeType, topic string,
	details models.JSONMap, body []byte,
) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.createErr != nil {
		return "", f.createErr
	}

	f.created = append(f.created, createdAttachment{
		orgUID: orgUID.String(), name: name, mimeType: mimeType,
		topic: topic, details: details, body: body,
	})

	return "file-" + uuid.New().String(), nil
}

func (f *fakeAttachmentStore) DeleteAttachments(
	_ context.Context, _, prefix string,
) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.deleteErr != nil {
		return 0, f.deleteErr
	}

	f.deleted = append(f.deleted, prefix)

	return 1, nil
}

func (f *fakeAttachmentStore) ListAttachmentViews(
	_ context.Context, _, _, _ string,
) ([]attachments.View, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.views, nil
}

func (f *fakeAttachmentStore) snapshot() ([]createdAttachment, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]createdAttachment(nil), f.created...), append([]string(nil), f.deleted...)
}

// shotPNG is a fixture that starts with the real PNG magic.
//
// errStorageDown stands in for an unreachable storage backend.
var errStorageDown = errors.New("object storage is down")

//nolint:gochecknoglobals // an immutable fixture Go cannot express as const
var shotPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 's', 'h', 'o', 't'}

// screenshotDownResult is a failing result carrying an in-memory capture, the
// shape checkbrowser produces for a failing browser check with the flag set.
func screenshotDownResult(orgUID, checkUID string, capturedAt time.Time) *models.Result {
	result := downResult(orgUID, checkUID, "keyword check failed")
	result.Diagnostics = &checkerdef.Diagnostics{
		Screenshot: &checkerdef.Screenshot{PNG: shotPNG, CapturedAt: capturedAt},
	}

	return result
}

// TestCreateIncidentPersistsScreenshot is the headline: an incident opened by
// a captured failure gets the PNG filed under its own topic, with the details
// bag the spec fixes.
func TestCreateIncidentPersistsScreenshot(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	store := &fakeAttachmentStore{}
	s.svc.SetAttachmentStore(store)

	capturedAt := time.Now().UTC().Truncate(time.Second)
	result := screenshotDownResult(s.org.UID, s.check.UID, capturedAt)
	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check, result))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)

	created, deleted := store.snapshot()
	r.Len(created, 1)
	r.Empty(deleted, "an open has no previous evidence to clear")

	attachment := created[0]
	r.Equal(s.org.UID, attachment.orgUID)
	r.Equal("image/png", attachment.mimeType)
	r.Equal(shotPNG, attachment.body)

	// The topic IS the link — no uid is stored on the incident row itself.
	r.Equal(
		models.AttachmentTopic(models.AttachmentEntityIncidents, inc.UID, models.AttachmentKindScreenshot),
		attachment.topic,
	)

	r.Equal(capturedAt, attachment.details[models.AttachmentDetailCapturedAt])
	r.Equal(s.check.UID, attachment.details[models.AttachmentDetailCheckUID])
	r.Equal(models.AttachmentTriggerIncidentOpen, attachment.details[models.AttachmentDetailTrigger])
	// Region comes from the persisted RESULT row, never from the probe.
	r.Equal("eu", attachment.details[models.AttachmentDetailRegion])
}

// TestScreenshotDroppedOnNonTransitionFailures proves the negative that bounds
// the whole storage story: only a transition persists.
//
// Driven with a flapping sequence rather than by calling createIncident twice,
// because "every other failing run is dropped on the floor" is a property of
// the real state machine, not of the helper.
func TestScreenshotDroppedOnNonTransitionFailures(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	store := &fakeAttachmentStore{}
	s.svc.SetAttachmentStore(store)

	// First failure opens the incident: one attachment.
	r.NoError(s.svc.CreateIncidentForTest(
		ctx, s.check, screenshotDownResult(s.org.UID, s.check.UID, time.Now())))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)

	created, _ := store.snapshot()
	r.Len(created, 1, "positive control: the open really did persist one")

	// Three more failing results while the SAME incident stays open. Each one
	// carries a capture; none of them is a transition.
	for range 3 {
		result := screenshotDownResult(s.org.UID, s.check.UID, time.Now())
		r.NoError(s.svc.ProcessCheckResult(ctx, s.check, result))
	}

	created, _ = store.snapshot()
	r.Len(created, 1, "a failing run that opens nothing writes nothing")

	// And the incident is still the same one — the loop above really did run
	// against an open incident rather than opening new ones.
	still, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)
	r.Equal(inc.UID, still.UID)
}

// TestReopenReplacesPreviousScreenshot pins the overwrite rule: a relapse is a
// new onset, so the old evidence goes first and the new one is filed under the
// same topic.
func TestReopenReplacesPreviousScreenshot(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	store := &fakeAttachmentStore{}
	s.svc.SetAttachmentStore(store)

	r.NoError(s.svc.CreateIncidentForTest(
		ctx, s.check, screenshotDownResult(s.org.UID, s.check.UID, time.Now())))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)

	resolveIncidentForReopen(t, s, inc.UID)

	// The relapse, with a capture of its own.
	relapseAt := time.Now().UTC().Truncate(time.Second)
	r.NoError(s.svc.CreateOrReopenIncidentForTest(
		ctx, s.check, screenshotDownResult(s.org.UID, s.check.UID, relapseAt)))

	created, deleted := store.snapshot()

	prefix := models.AttachmentTopicPrefix(models.AttachmentEntityIncidents, inc.UID)
	r.Equal([]string{prefix}, deleted, "the previous onset's evidence is cleared first")

	r.Len(created, 2)
	r.Equal(models.AttachmentTriggerIncidentReopen,
		created[1].details[models.AttachmentDetailTrigger])
	r.Equal(relapseAt, created[1].details[models.AttachmentDetailCapturedAt])
	// Same topic: the incident is the same incident, the evidence is replaced.
	r.Equal(created[0].topic, created[1].topic)
}

// TestReopenWithoutCaptureStillDropsTheStaleOne is the harder half of the
// overwrite rule, and mirrors what lastFailureDetails does to a stale
// failureResponse: a relapse that produced NO capture must not leave the
// previous onset's screenshot sitting next to it. A picture of a 503 page
// beside an incident whose current onset is a DNS timeout is worse than no
// picture at all.
func TestReopenWithoutCaptureStillDropsTheStaleOne(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	store := &fakeAttachmentStore{}
	s.svc.SetAttachmentStore(store)

	r.NoError(s.svc.CreateIncidentForTest(
		ctx, s.check, screenshotDownResult(s.org.UID, s.check.UID, time.Now())))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)

	resolveIncidentForReopen(t, s, inc.UID)

	// The relapse carries no capture at all (a DNS timeout never rendered).
	r.NoError(s.svc.CreateOrReopenIncidentForTest(
		ctx, s.check, downResult(s.org.UID, s.check.UID, "dns timeout")))

	created, deleted := store.snapshot()
	r.Len(created, 1, "no new evidence was written")
	r.Equal(
		[]string{models.AttachmentTopicPrefix(models.AttachmentEntityIncidents, inc.UID)},
		deleted,
		"but the stale evidence was still cleared",
	)
}

// TestScreenshotPersistenceIsBestEffort pins the rule the whole feature hangs
// off: nothing the attachments rail does may fail an incident transition.
func TestScreenshotPersistenceIsBestEffort(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	s.svc.SetAttachmentStore(&fakeAttachmentStore{
		createErr: errStorageDown,
		deleteErr: errStorageDown,
	})

	result := screenshotDownResult(s.org.UID, s.check.UID, time.Now())
	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check, result), "the incident still opens")

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)
	r.NotNil(inc, "and it is a real, queryable incident")
}

// TestNoAttachmentStoreIsSafe covers the deployment where file storage is not
// wired at all: captures travel as far as the pipeline and are dropped.
func TestNoAttachmentStoreIsSafe(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	// Deliberately no SetAttachmentStore call.
	result := screenshotDownResult(s.org.UID, s.check.UID, time.Now())
	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check, result))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)
	r.NotNil(inc)
}

// TestGetIncidentSurfacesAttachments proves the detail response carries the
// attachment views the store returns — with the signed URL — and that they do
// not leak into the incident's own details blob.
func TestGetIncidentSurfacesAttachments(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	store := &fakeAttachmentStore{views: []attachments.View{{
		UID:         "file-1",
		Name:        "api-screenshot.png",
		Kind:        models.AttachmentKindScreenshot,
		MimeType:    "image/png",
		Size:        4096,
		DownloadURL: "https://acme.com/pub/files/file-1?exp=1&sig=deadbeef",
		Details:     models.JSONMap{models.AttachmentDetailRegion: "eu"},
		CreatedAt:   time.Now(),
	}}}
	s.svc.SetAttachmentStore(store)

	r.NoError(s.svc.CreateIncidentForTest(
		ctx, s.check, screenshotDownResult(s.org.UID, s.check.UID, time.Now())))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)

	resp, err := s.svc.GetIncident(ctx, s.org.Slug, inc.UID, nil)
	r.NoError(err)

	r.Len(resp.Attachments, 1)
	r.Equal("file-1", resp.Attachments[0].UID)
	r.Equal(models.AttachmentKindScreenshot, resp.Attachments[0].Kind)
	r.Contains(resp.Attachments[0].DownloadURL, "sig=deadbeef")

	// The attachment is its own field, never smuggled into details.
	_, inDetails := resp.Details["attachments"]
	r.False(inDetails)
}
