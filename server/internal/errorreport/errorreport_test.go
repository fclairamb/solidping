package errorreport_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/analytics/analyticstest"
	"github.com/fclairamb/solidping/server/internal/errorreport"
	"github.com/fclairamb/solidping/server/internal/errorreport/errorreporttest"
)

var errBoom = errors.New("boom")

func TestCaptureOnRequest_ReportsOnTheRequestHub(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	hub, transport, err := errorreporttest.NewHub(t)
	r.NoError(err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orgs/acme/checks", http.NoBody)
	req = req.WithContext(sentry.SetHubOnContext(req.Context(), hub))

	errorreport.CaptureOnRequest(req, errBoom)

	events := transport.Events()
	r.Len(events, 1)
	r.Equal("boom", events[0].Exception[0].Value)
}

// Positive control for the test above: without a hub on the request there is
// nothing to report on, and the capture must stay silent rather than leaking
// onto the process-wide hub.
func TestCaptureOnRequest_NoHubOnRequestIsANoOp(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, transport, err := errorreporttest.NewHub(t)
	r.NoError(err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orgs/acme/checks", http.NoBody)

	// Must not panic, must not report anywhere we can observe.
	errorreport.CaptureOnRequest(req, errBoom)
	errorreport.CaptureOnRequest(nil, errBoom)
	errorreport.CaptureOnRequest(req, nil)

	r.Equal(0, transport.Len())
}

func TestCaptureWithTags_TagsTheEventAndSkipsEmptyValues(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	hub, transport, err := errorreporttest.NewHub(t)
	r.NoError(err)

	ctx := sentry.SetHubOnContext(t.Context(), hub)

	errorreport.CaptureWithTags(ctx, errBoom, map[string]string{
		"check.type":   "http",
		"check.uid":    "check-uid-1",
		"organization": "",
	})

	events := transport.Events()
	r.Len(events, 1)
	r.Equal("http", events[0].Tags["check.type"])
	r.Equal("check-uid-1", events[0].Tags["check.uid"])
	r.NotContains(events[0].Tags, "organization", "an empty value must not become a \"\" facet")
}

// CaptureWithTags must not leak the tags onto the hub's own scope: a worker
// hub is long-lived, and a tag set for one crashed check would otherwise
// follow every later event.
func TestCaptureWithTags_DoesNotLeakTagsOntoTheHubScope(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	hub, transport, err := errorreporttest.NewHub(t)
	r.NoError(err)

	ctx := sentry.SetHubOnContext(t.Context(), hub)

	errorreport.CaptureWithTags(ctx, errBoom, map[string]string{"check.uid": "first"})
	errorreport.CaptureWithTags(ctx, errBoom, nil)

	events := transport.Events()
	r.Len(events, 2)
	r.Equal("first", events[0].Tags["check.uid"])
	r.NotContains(events[1].Tags, "check.uid")
}

// The background-worker fallback: no hub on the context, so reporting goes to
// the hub sentry.Init configured. This is the shape production actually runs —
// check and job workers have no middleware to install a hub.
//
//nolint:paralleltest // binds the process-wide Sentry client
func TestCaptureWithTags_FallsBackToTheCurrentHub(t *testing.T) {
	r := require.New(t)

	client, transport, err := errorreporttest.NewClient(t)
	r.NoError(err)

	previous := sentry.CurrentHub().Client()
	sentry.CurrentHub().BindClient(client)

	t.Cleanup(func() { sentry.CurrentHub().BindClient(previous) })

	errorreport.CaptureWithTags(t.Context(), errBoom, map[string]string{"job.type": "aggregation"})

	events := transport.Events()
	r.Len(events, 1)
	r.Equal("aggregation", events[0].Tags["job.type"])
}

func newReq(t *testing.T) *http.Request {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orgs/acme/checks?token=zzz", http.NoBody)
	req.Header.Set(errorreport.HeaderSessionID, "sess-1")
	req.Header.Set(errorreport.HeaderDistinctID, "dist-1")

	return req
}

//nolint:paralleltest // binds the process-wide analytics client
func TestCaptureOnRequest_ReportsToBothSinks(t *testing.T) {
	r := require.New(t)
	ph := analyticstest.Install(t)

	hub, transport, err := errorreporttest.NewHub(t)
	r.NoError(err)

	req := newReq(t)
	req = req.WithContext(sentry.SetHubOnContext(req.Context(), hub))

	errorreport.CaptureOnRequest(req, errBoom)

	r.Equal(1, transport.Len())

	excs := ph.Exceptions()
	r.Len(excs, 1)
	r.Equal("sess-1", excs[0].SessionID)
	r.Equal("dist-1", excs[0].DistinctID)
	r.Equal(http.MethodGet, excs[0].Tags["http.method"])
}

//nolint:paralleltest // binds the process-wide analytics client
func TestCaptureOnRequest_NoHubStillReachesPostHog(t *testing.T) {
	r := require.New(t)
	ph := analyticstest.Install(t)

	errorreport.CaptureOnRequest(newReq(t), errBoom)
	r.Len(ph.Exceptions(), 1)
}

//nolint:paralleltest // binds the process-wide analytics client
func TestCaptureOnRequest_PostHogOffOnlySentry(t *testing.T) {
	r := require.New(t)

	hub, transport, err := errorreporttest.NewHub(t)
	r.NoError(err)

	req := newReq(t)
	req = req.WithContext(sentry.SetHubOnContext(req.Context(), hub))

	errorreport.CaptureOnRequest(req, errBoom)
	r.Equal(1, transport.Len())
}

//nolint:paralleltest // binds the process-wide analytics client
func TestCaptureOnRequest_IdentityAndMalformedHeaders(t *testing.T) {
	r := require.New(t)
	ph := analyticstest.Install(t)

	req := newReq(t)
	req.Header.Set(errorreport.HeaderSessionID, "bad value\n<script>")
	req = req.WithContext(errorreport.WithIdentityHolder(req.Context()))
	errorreport.SetIdentity(req.Context(), "o1", "u1")

	errorreport.CaptureOnRequest(req, errBoom)

	excs := ph.Exceptions()
	r.Len(excs, 1)
	r.Empty(excs[0].SessionID)
	r.Empty(excs[0].DistinctID)
	r.Equal("o1", excs[0].OrgUID)
	r.Equal("u1", excs[0].UserUID)
}

//nolint:paralleltest // binds the process-wide analytics client
func TestCaptureWithTags_ReachesPostHogWithOrgDistinctID(t *testing.T) {
	r := require.New(t)
	ph := analyticstest.Install(t)

	errorreport.CaptureWithTags(t.Context(), errBoom, map[string]string{"organization": "o9", "job.type": "x"})
	errorreport.CaptureWithTags(t.Context(), errBoom, map[string]string{"job.type": "y"})

	excs := ph.Exceptions()
	r.Len(excs, 2)
	r.Equal("o9", excs[0].OrgUID)
	r.Equal("server", excs[1].DistinctID)
}
