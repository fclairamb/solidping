package incidents_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

// fakeAttachmentStore records every call the incident pipeline makes, so a test
// can assert not only WHAT was stored but how many times the pipeline decided
// to store anything at all — which is the whole "transitions only" claim.
type fakeAttachmentStore struct {
	mu sync.Mutex

	puts    []fakePut
	deletes []string
	// failPut makes the store report an error, to prove the pipeline survives
	// a storage outage.
	failPut error
}

type fakePut struct {
	incidentUID string
	png         []byte
	details     models.JSONMap
}

func (f *fakeAttachmentStore) PutIncidentScreenshot(
	_ context.Context, _, incidentUID string, png []byte, details models.JSONMap,
) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.failPut != nil {
		return "", f.failPut
	}

	f.puts = append(f.puts, fakePut{incidentUID: incidentUID, png: png, details: details})

	return "file-" + incidentUID, nil
}

func (f *fakeAttachmentStore) DeleteIncidentAttachments(
	_ context.Context, _, incidentUID string,
) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.deletes = append(f.deletes, incidentUID)

	return 1, nil
}

func (f *fakeAttachmentStore) ListIncidentAttachments(
	_ context.Context, _, _ string,
) ([]attachments.Response, error) {
	return nil, nil
}

func (f *fakeAttachmentStore) snapshot() ([]fakePut, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]fakePut(nil), f.puts...), append([]string(nil), f.deletes...)
}

// screenshotPNG is a recognizable blob; nothing decodes it.
func screenshotPNG(marker string) []byte {
	return append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte(marker)...)
}

// shotDownResult is a failing result carrying an in-process browser capture.
func shotDownResult(orgUID, checkUID, marker string) *models.Result {
	result := downResult(orgUID, checkUID, "keyword check failed")
	result.Diagnostics = &checkerdef.Diagnostics{
		Screenshot: &checkerdef.Screenshot{
			PNG:        screenshotPNG(marker),
			CapturedAt: time.Now().UTC(),
		},
	}

	return result
}

// TestCreateIncidentPersistsScreenshot is the headline: the capture that opened
// the incident is written under that incident, with the region stamped
// SERVER-SIDE from the result row.
func TestCreateIncidentPersistsScreenshot(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	store := &fakeAttachmentStore{}
	s.svc.SetAttachmentStore(store)

	result := shotDownResult(s.org.UID, s.check.UID, "onset")
	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check, result))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)

	puts, _ := store.snapshot()
	r.Len(puts, 1)
	r.Equal(inc.UID, puts[0].incidentUID)
	r.Equal(screenshotPNG("onset"), puts[0].png)
	r.Equal(attachments.TriggerIncidentOpen, puts[0].details[attachments.DetailKeyTrigger])
	r.Equal(s.check.UID, puts[0].details[attachments.DetailKeyCheckUID])
	r.Equal("eu", puts[0].details[attachments.DetailKeyRegion],
		"the region comes from the persisted result, never from the checker")
	r.NotEmpty(puts[0].details[attachments.DetailKeyCapturedAt])
}

// TestScreenshotOnlyPersistedOnTransitions is the negative the storage math
// depends on: a failing run that opens nothing must persist nothing.
//
// Driven through the real ProcessCheckResult with a confirmation period long
// enough that the first failures cannot open an incident, then relaxed so the
// last one does — the positive control inside the same test.
func TestScreenshotOnlyPersistedOnTransitions(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	store := &fakeAttachmentStore{}
	s.svc.SetAttachmentStore(store)

	// A long confirmation period: failures accumulate, no incident opens.
	s.check.ConfirmationPeriodSeconds = 3600

	for range 5 {
		r.NoError(s.svc.ProcessCheckResult(ctx, s.check,
			shotDownResult(s.org.UID, s.check.UID, "not-a-transition")))
	}

	puts, _ := store.snapshot()
	r.Empty(puts, "a failing run that opens no incident must drop its capture on the floor")

	// Positive control: with the threshold satisfied the very same result shape
	// DOES get persisted, so the assertion above is about the transition rule
	// and not about a store that is never called.
	s.check.ConfirmationPeriodSeconds = 0
	r.NoError(s.svc.ProcessCheckResult(ctx, s.check,
		shotDownResult(s.org.UID, s.check.UID, "transition")))

	puts, _ = store.snapshot()
	r.Len(puts, 1)
	r.Equal(screenshotPNG("transition"), puts[0].png)
}

// TestReopenReplacesScreenshot pins the reopen rule: the relapse's capture
// becomes the incident's evidence, and the previous one is retired first.
func TestReopenReplacesScreenshot(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	store := &fakeAttachmentStore{}
	s.svc.SetAttachmentStore(store)

	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check,
		shotDownResult(s.org.UID, s.check.UID, "first-onset")))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)

	// Resolve it so the next failure reopens rather than opening a new one.
	resolved := models.IncidentStateResolved
	resolvedAt := time.Now()
	r.NoError(s.dbSvc.UpdateIncident(ctx, inc.UID, &models.IncidentUpdate{
		State: &resolved, ResolvedAt: &resolvedAt,
	}))

	r.NoError(s.svc.CreateOrReopenIncidentForTest(ctx, s.check,
		shotDownResult(s.org.UID, s.check.UID, "relapse")))

	reopened, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)
	r.Equal(inc.UID, reopened.UID, "the same incident must have been reopened")

	puts, deletes := store.snapshot()
	r.Len(puts, 2)
	r.Equal(screenshotPNG("relapse"), puts[1].png)
	r.Equal(attachments.TriggerIncidentReopen, puts[1].details[attachments.DetailKeyTrigger])

	// The pipeline does NOT issue a separate delete when the relapse brought
	// its own capture: the write is itself a replace (see
	// TestPutIncidentScreenshotReplaces in the attachments package), and an
	// extra delete would open a window in which the incident has no evidence
	// at all. The delete path is for the no-capture relapse — next test.
	r.Empty(deletes)
}

// TestReopenWithoutCaptureDropsTheStaleOne is the sharper half of the reopen
// rule: a relapse that produced NO capture must not leave the original outage's
// screenshot sitting next to a different failure.
func TestReopenWithoutCaptureDropsTheStaleOne(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	store := &fakeAttachmentStore{}
	s.svc.SetAttachmentStore(store)

	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check,
		shotDownResult(s.org.UID, s.check.UID, "first-onset")))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)

	resolved := models.IncidentStateResolved
	resolvedAt := time.Now()
	r.NoError(s.dbSvc.UpdateIncident(ctx, inc.UID, &models.IncidentUpdate{
		State: &resolved, ResolvedAt: &resolvedAt,
	}))

	// The relapse is a DNS timeout: no page, no capture.
	r.NoError(s.svc.CreateOrReopenIncidentForTest(ctx, s.check,
		downResult(s.org.UID, s.check.UID, "dial tcp: i/o timeout")))

	puts, deletes := store.snapshot()
	r.Len(puts, 1, "no new capture was written")
	r.Contains(deletes, inc.UID, "the stale capture must be deleted, not left behind")
}

// TestScreenshotStorageFailureNeverBlocksTheIncident pins the best-effort
// contract: an attachment store that is down must not stop an incident from
// opening. A missing screenshot is a papercut; a missing incident is an outage
// nobody is paged for.
func TestScreenshotStorageFailureNeverBlocksTheIncident(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	s.svc.SetAttachmentStore(&fakeAttachmentStore{failPut: context.DeadlineExceeded})

	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check,
		shotDownResult(s.org.UID, s.check.UID, "onset")))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)
	r.NotNil(inc)
}

// TestAgentMarkerCarriesNoBytesToPersist covers the agent path's shape: the
// result frame advertises a capture (available + captureId) without the bytes,
// so the pipeline must NOT invent an empty attachment for it.
func TestAgentMarkerCarriesNoBytesToPersist(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	store := &fakeAttachmentStore{}
	s.svc.SetAttachmentStore(store)

	result := downResult(s.org.UID, s.check.UID, "keyword check failed")
	result.Diagnostics = &checkerdef.Diagnostics{
		Screenshot: &checkerdef.Screenshot{Available: true, CaptureID: "cap-1"},
	}

	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check, result))

	puts, _ := store.snapshot()
	r.Empty(puts, "a marker with no bytes must not become a zero-byte attachment")
}

// TestNoAttachmentStoreIsSafe pins the nil-safety: a deployment with no
// attachment store wired still opens incidents normally.
func TestNoAttachmentStoreIsSafe(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check,
		shotDownResult(s.org.UID, s.check.UID, "onset")))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)
	r.NotNil(inc)
}

// compile-time check that the fake satisfies the real interface.
var _ incidents.AttachmentStore = (*fakeAttachmentStore)(nil)
