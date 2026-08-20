package errorreport_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getsentry/sentry-go"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/errorreport"
	"github.com/fclairamb/solidping/server/internal/errorreport/errorreporttest"
)

var errBoom = errors.New("boom")

func TestCaptureOnRequest_ReportsOnTheRequestHub(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	hub, transport, err := errorreporttest.NewHub()
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

	_, transport, err := errorreporttest.NewHub()
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

	hub, transport, err := errorreporttest.NewHub()
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

	hub, transport, err := errorreporttest.NewHub()
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

	client, transport, err := errorreporttest.NewClient()
	r.NoError(err)

	previous := sentry.CurrentHub().Client()
	sentry.CurrentHub().BindClient(client)

	t.Cleanup(func() { sentry.CurrentHub().BindClient(previous) })

	errorreport.CaptureWithTags(t.Context(), errBoom, map[string]string{"job.type": "aggregation"})

	events := transport.Events()
	r.Len(events, 1)
	r.Equal("aggregation", events[0].Tags["job.type"])
}
