package incidents_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
)

// fakeUploadRequester records every upload request the incident pipeline
// emits. Counting them is the whole point: the spec's "bounding the ask"
// directive is a statement about HOW MANY requests a chatty agent can provoke,
// not about whether one is ever sent.
type fakeUploadRequester struct {
	mu   sync.Mutex
	asks []uploadAsk
}

type uploadAsk struct {
	workerUID string
	captureID string
	topic     string
}

func (f *fakeUploadRequester) RequestScreenshotUpload(
	_ context.Context, workerUID, captureID, topic string,
) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.asks = append(f.asks, uploadAsk{workerUID: workerUID, captureID: captureID, topic: topic})
}

func (f *fakeUploadRequester) snapshot() []uploadAsk {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]uploadAsk(nil), f.asks...)
}

// markerDownResult is a failing result the way a DEPORTED agent sends it: the
// screenshot is advertised (available + captureId) and the bytes are not here.
func markerDownResult(orgUID, checkUID, captureID, workerUID string) *models.Result {
	result := downResult(orgUID, checkUID, "keyword check failed")
	result.WorkerUID = &workerUID
	result.Diagnostics = &checkerdef.Diagnostics{
		Screenshot: &checkerdef.Screenshot{
			Available:  true,
			CaptureID:  captureID,
			CapturedAt: time.Now().UTC(),
		},
	}

	return result
}

// TestMarkerOnOpenRequestsTheUpload is the positive control for every negative
// below: a marker-carrying result that OPENS an incident asks the agent that
// produced it for the bytes, under a topic built from the incident row the
// server just wrote.
func TestMarkerOnOpenRequestsTheUpload(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	requester := &fakeUploadRequester{}
	s.svc.SetAgentUploadRequester(requester)
	s.svc.SetAttachmentStore(&fakeAttachmentStore{})

	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check,
		markerDownResult(s.org.UID, s.check.UID, "cap-open", "worker-1")))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)

	asks := requester.snapshot()
	r.Len(asks, 1)
	r.Equal("worker-1", asks[0].workerUID, "the ask goes to the worker that produced the result")
	r.Equal("cap-open", asks[0].captureID)
	r.Equal(attachments.IncidentScreenshotTopic(inc.UID), asks[0].topic)

	// The topic is SERVER-GENERATED from the incident, so it must name the
	// incident that just opened and nothing the agent could have chosen.
	r.True(strings.HasPrefix(asks[0].topic, attachments.IncidentTopicPrefix(inc.UID)))
}

// TestUploadRequestBoundedToOnePerTransition is the spec's "bounding the ask"
// directive, as a real negative.
//
// A chatty agent marks EVERY failing result, and the target FLAPS: down, up,
// down, up… Because the recovery period is longer than the flap, the state
// machine opens exactly ONE incident across the whole sequence — so exactly one
// upload request may be emitted. If the request were driven by the marker
// rather than by the transition, this would be five.
func TestUploadRequestBoundedToOnePerTransition(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	requester := &fakeUploadRequester{}
	s.svc.SetAgentUploadRequester(requester)

	// Open immediately on a failure, but never auto-resolve inside the test's
	// wall-clock: the flap must not become a resolve+reopen, which would be a
	// legitimate second transition and would hide the bug this test hunts.
	s.check.ConfirmationPeriodSeconds = 0
	s.check.RecoveryPeriodSeconds = 3600

	for i := range 5 {
		r.NoError(s.svc.ProcessCheckResult(ctx, s.check,
			markerDownResult(s.org.UID, s.check.UID, "cap-"+string(rune('a'+i)), "worker-1")))

		// The flap back up: the incident stays open (recovery period unmet), so
		// the next failure is not a transition either.
		up := models.NewResult(s.org.UID, s.check.UID, models.ResultStatusUp, 0)
		r.NoError(s.svc.ProcessCheckResult(ctx, s.check, up))
	}

	opened, _, err := s.dbSvc.ListIncidents(ctx, &models.ListIncidentsFilter{
		OrganizationUID: s.org.UID, CheckUIDs: []string{s.check.UID},
	})
	r.NoError(err)
	r.Len(opened, 1, "the flapping sequence must have opened exactly one incident")

	asks := requester.snapshot()
	r.Len(asks, 1, "one incident transition => exactly one upload request, never one per result")
	r.Equal("cap-a", asks[0].captureID, "the ask names the capture of the result that OPENED it")
}

// TestNoUploadRequestWithoutATransition is the other half of the bound: failing
// results that open nothing must ask for nothing at all.
//
// A long confirmation period keeps the state machine below its threshold; the
// positive control at the end relaxes it so the very same result shape DOES
// produce a request, proving the assertion is about the transition rule and not
// about a requester that is never called.
func TestNoUploadRequestWithoutATransition(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	requester := &fakeUploadRequester{}
	s.svc.SetAgentUploadRequester(requester)

	s.check.ConfirmationPeriodSeconds = 3600

	for range 5 {
		r.NoError(s.svc.ProcessCheckResult(ctx, s.check,
			markerDownResult(s.org.UID, s.check.UID, "cap-x", "worker-1")))
	}

	r.Empty(requester.snapshot(), "a failing run that opens no incident must ask for nothing")

	s.check.ConfirmationPeriodSeconds = 0
	r.NoError(s.svc.ProcessCheckResult(ctx, s.check,
		markerDownResult(s.org.UID, s.check.UID, "cap-open", "worker-1")))

	asks := requester.snapshot()
	r.Len(asks, 1)
	r.Equal("cap-open", asks[0].captureID)
}

// TestReopenRequestsTheRelapseCapture pins the second (and only other)
// transition: a relapse asks for ITS own capture, under the same incident's
// topic, so the reopened incident shows the failure it currently has.
func TestReopenRequestsTheRelapseCapture(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	requester := &fakeUploadRequester{}
	s.svc.SetAgentUploadRequester(requester)

	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check,
		markerDownResult(s.org.UID, s.check.UID, "cap-first", "worker-1")))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)

	resolved := models.IncidentStateResolved
	resolvedAt := time.Now()
	r.NoError(s.dbSvc.UpdateIncident(ctx, inc.UID, &models.IncidentUpdate{
		State: &resolved, ResolvedAt: &resolvedAt,
	}))

	r.NoError(s.svc.CreateOrReopenIncidentForTest(ctx, s.check,
		markerDownResult(s.org.UID, s.check.UID, "cap-relapse", "worker-1")))

	reopened, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)
	r.Equal(inc.UID, reopened.UID)

	asks := requester.snapshot()
	r.Len(asks, 2, "open and reopen are the two transitions, and the only two")
	r.Equal("cap-relapse", asks[1].captureID)
	r.Equal(attachments.IncidentScreenshotTopic(inc.UID), asks[1].topic)
}

// TestInProcessCaptureNeverAsksTheAgent is the negative that keeps the two
// paths apart: the in-process worker hands over the actual bytes, so the
// pipeline must store them and ask nobody for anything.
func TestInProcessCaptureNeverAsksTheAgent(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	requester := &fakeUploadRequester{}
	store := &fakeAttachmentStore{}
	s.svc.SetAgentUploadRequester(requester)
	s.svc.SetAttachmentStore(store)

	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check,
		shotDownResult(s.org.UID, s.check.UID, "in-process")))

	puts, _ := store.snapshot()
	r.Len(puts, 1, "the in-process path stores the bytes it already has")
	r.Empty(requester.snapshot(), "and must never ask an agent to send what it already sent")
}

// TestIncompleteMarkerAsksNothing covers the shapes that name no fetchable
// capture: available without an id, an id without available, and a result with
// no worker uid to address the request to.
func TestIncompleteMarkerAsksNothing(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()

	cases := []struct {
		name  string
		mutID func(*models.Result)
	}{
		{"available without a capture id", func(res *models.Result) {
			res.Diagnostics.Screenshot.CaptureID = ""
		}},
		{"capture id without available", func(res *models.Result) {
			res.Diagnostics.Screenshot.Available = false
		}},
		{"no worker to address", func(res *models.Result) {
			res.WorkerUID = nil
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s := newFailureSnapshotSetup(t)
			requester := &fakeUploadRequester{}
			s.svc.SetAgentUploadRequester(requester)

			result := markerDownResult(s.org.UID, s.check.UID, "cap-1", "worker-1")
			tc.mutID(result)

			r.NoError(s.svc.CreateIncidentForTest(ctx, s.check, result))

			inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
			r.NoError(err)
			r.NotNil(inc, "the incident opens regardless")

			r.Empty(requester.snapshot())
		})
	}
}

// TestNoUploadRequesterIsSafe pins the nil-safety: every deployment that has no
// agent transport at all (and every existing test) still opens incidents with a
// marker-carrying result and no requester wired.
func TestNoUploadRequesterIsSafe(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check,
		markerDownResult(s.org.UID, s.check.UID, "cap-1", "worker-1")))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)
	r.NotNil(inc)
}
