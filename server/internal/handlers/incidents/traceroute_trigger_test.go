package incidents_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/attachments"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
)

// fakeTraceRequester records every trace the pipeline asks for. The dispatcher
// itself is tested in handlers/tracediag; what is under test here is the GATE
// — when the pipeline decides to ask at all.
type fakeTraceRequester struct {
	mu       sync.Mutex
	requests []*incidents.TraceRequest
}

func (f *fakeTraceRequester) RequestTrace(_ context.Context, req *incidents.TraceRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.requests = append(f.requests, req)
}

func (f *fakeTraceRequester) snapshot() []*incidents.TraceRequest {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]*incidents.TraceRequest(nil), f.requests...)
}

// netDownResult is a failing result carrying the checker's network-reachability
// marker — the thing that makes a failure traceable.
func netDownResult(orgUID, checkUID, class string) *models.Result {
	result := downResult(orgUID, checkUID, "connection refused")
	result.Diagnostics = &checkerdef.Diagnostics{
		NetworkFailure: &checkerdef.NetworkFailure{
			Class:   class,
			Host:    "acme.com",
			Address: "192.0.2.10",
			Port:    443,
		},
	}

	return result
}

// TestCreateIncidentRequestsATrace is the headline: a network failure that
// opens an incident asks for a path capture, with a SERVER-GENERATED topic and
// the region taken from the persisted result row.
func TestCreateIncidentRequestsATrace(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	traces := &fakeTraceRequester{}
	s.svc.SetTraceRequester(traces)

	result := netDownResult(s.org.UID, s.check.UID, checkerdef.NetFailureConnectionRefused)
	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check, result))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)

	got := traces.snapshot()
	r.Len(got, 1)
	r.Equal(inc.UID, got[0].IncidentUID)
	r.Equal(attachments.IncidentTracerouteTopic(inc.UID), got[0].Topic,
		"the topic must be built from the incident row the server just wrote")
	r.Equal(attachments.TriggerIncidentOpen, got[0].Trigger)
	r.Equal("eu", got[0].Region, "the region comes from the persisted result, never from the checker")
	r.Equal("192.0.2.10", got[0].Failure.Address)
	r.Equal(443, got[0].Failure.Port)
	r.Equal(s.check.UID, got[0].CheckUID)
}

// TestApplicationFailureRequestsNoTrace is THE negative of the trigger policy.
//
// The result is DOWN and an incident opens — but it carries no
// network-reachability marker, which is what an HTTP 500, a keyword mismatch or
// an expiring certificate looks like on this path. A traceroute would be noise
// on the incident page, and the check's path is provably fine.
func TestApplicationFailureRequestsNoTrace(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	traces := &fakeTraceRequester{}
	s.svc.SetTraceRequester(traces)

	// A 500 with `capture_failure_response` on: Diagnostics EXISTS and is
	// populated, it simply carries no reachability marker. Testing the
	// no-Diagnostics case alone would pass even if the marker check were
	// deleted, because the nil-Diagnostics guard would catch it first.
	appFailure := downResult(s.org.UID, s.check.UID, "unexpected status code: 500")
	appFailure.Diagnostics = &checkerdef.Diagnostics{
		FailureResponse: &checkerdef.FailureResponse{
			URL:        "https://acme.com/health",
			StatusCode: 500,
			StatusLine: "HTTP/2.0 500 Internal Server Error",
			Body:       "boom",
		},
	}

	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check, appFailure))

	r.Empty(traces.snapshot(), "an application-level failure must not trigger a path trace")

	// Positive control: the SAME pipeline, the same check, one marker later.
	r.NoError(s.svc.ProcessCheckResult(ctx, s.check,
		netDownResult(s.org.UID, s.check.UID, checkerdef.NetFailureConnectTimeout)))

	// (The incident is already open, so drive a fresh transition instead.)
	if len(traces.snapshot()) == 0 {
		other := newFailureSnapshotSetup(t)
		other.svc.SetTraceRequester(traces)
		r.NoError(other.svc.CreateIncidentForTest(ctx, other.check,
			netDownResult(other.org.UID, other.check.UID, checkerdef.NetFailureConnectTimeout)))
	}

	r.NotEmpty(traces.snapshot(),
		"the control failed: a marked failure must trigger a trace, or the negative above proves nothing")
}

// TestMarkerWithoutAnAddressRequestsNoTrace: a marker whose address could not
// be observed has nothing to trace to, and re-resolving the hostname could
// point at a machine that never failed.
func TestMarkerWithoutAnAddressRequestsNoTrace(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	traces := &fakeTraceRequester{}
	s.svc.SetTraceRequester(traces)

	result := netDownResult(s.org.UID, s.check.UID, checkerdef.NetFailureConnectTimeout)
	result.Diagnostics.NetworkFailure.Address = ""

	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check, result))
	r.Empty(traces.snapshot())
}

// TestTraceOnlyRequestedOnTransitions is the volume guarantee: a check failing
// every 30 s must not fork a 15-second sweep every 30 s.
func TestTraceOnlyRequestedOnTransitions(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	traces := &fakeTraceRequester{}
	s.svc.SetTraceRequester(traces)

	// A long confirmation period: failures accumulate, no incident opens.
	s.check.ConfirmationPeriodSeconds = 3600

	for range 5 {
		r.NoError(s.svc.ProcessCheckResult(ctx, s.check,
			netDownResult(s.org.UID, s.check.UID, checkerdef.NetFailureConnectTimeout)))
	}

	r.Empty(traces.snapshot(), "failing runs that open no incident must ask for nothing")

	// Positive control inside the same test: with the threshold satisfied the
	// very same result shape DOES trigger one request.
	s.check.ConfirmationPeriodSeconds = 0
	r.NoError(s.svc.ProcessCheckResult(ctx, s.check,
		netDownResult(s.org.UID, s.check.UID, checkerdef.NetFailureConnectTimeout)))

	r.Len(traces.snapshot(), 1)

	// And every FURTHER failing result on the now-open incident asks for
	// nothing more.
	for range 3 {
		r.NoError(s.svc.ProcessCheckResult(ctx, s.check,
			netDownResult(s.org.UID, s.check.UID, checkerdef.NetFailureConnectTimeout)))
	}

	r.Len(traces.snapshot(), 1, "an open incident must not keep re-requesting traces")
}

// TestPerCheckToggleOverridesTheOrgDefault — `off` on the check wins even
// though the org default is on.
func TestPerCheckToggleOverridesTheOrgDefault(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	traces := &fakeTraceRequester{}
	s.svc.SetTraceRequester(traces)

	disabled := false
	s.check.TracerouteOnFailure = &disabled

	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check,
		netDownResult(s.org.UID, s.check.UID, checkerdef.NetFailureConnectTimeout)))

	r.Empty(traces.snapshot(), "a check opted out must never be traced")
}

// TestOrgDefaultTurnsTracingOff pins the org-level switch AND its default.
//
// Two assertions in one test on purpose: the first proves the org parameter is
// actually read (set to false → nothing), the second that its ABSENCE means on
// (the spec's "org-level default (on)"), which no amount of testing the
// false case would establish.
func TestOrgDefaultTurnsTracingOff(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	traces := &fakeTraceRequester{}
	s.svc.SetTraceRequester(traces)

	r.NoError(s.dbSvc.SetOrgParameter(ctx, s.org.UID, models.ParamKeyTracerouteEnabled, false, false))

	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check,
		netDownResult(s.org.UID, s.check.UID, checkerdef.NetFailureConnectTimeout)))
	r.Empty(traces.snapshot(), "the org parameter was ignored")

	// Flip it back on and drive a fresh transition on a fresh org.
	fresh := newFailureSnapshotSetup(t)
	fresh.svc.SetTraceRequester(traces)

	r.NoError(fresh.svc.CreateIncidentForTest(ctx, fresh.check,
		netDownResult(fresh.org.UID, fresh.check.UID, checkerdef.NetFailureConnectTimeout)))
	r.Len(traces.snapshot(), 1, "an org that never set the parameter must default to ON")
}

// TestCheckOptInBeatsAnOrgOptOut — the third arm of the tri-state: an org that
// turned tracing off can still be overridden per check.
func TestCheckOptInBeatsAnOrgOptOut(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	traces := &fakeTraceRequester{}
	s.svc.SetTraceRequester(traces)

	r.NoError(s.dbSvc.SetOrgParameter(ctx, s.org.UID, models.ParamKeyTracerouteEnabled, false, false))

	enabled := true
	s.check.TracerouteOnFailure = &enabled

	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check,
		netDownResult(s.org.UID, s.check.UID, checkerdef.NetFailureConnectTimeout)))

	r.Len(traces.snapshot(), 1, "an explicit per-check opt-in must beat the org opt-out")
}

// TestNoTraceRequesterIsSafe — the pipeline must run identically on a
// deployment with no trace dispatcher wired at all.
func TestNoTraceRequesterIsSafe(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := context.Background()
	s := newFailureSnapshotSetup(t)

	r.NoError(s.svc.CreateIncidentForTest(ctx, s.check,
		netDownResult(s.org.UID, s.check.UID, checkerdef.NetFailureConnectTimeout)))

	inc, err := s.dbSvc.FindActiveIncidentByCheckUID(ctx, s.check.UID)
	r.NoError(err)
	r.NotNil(inc, "the incident must open with no trace dispatcher present")
}
