package notifications

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// gotifyFake captures every request posted by the sender (method, path,
// headers, and body) and answers with a caller-configured status code.
type gotifyFake struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   [][]byte
	status   int
}

func newGotifyFake(t *testing.T, status int) (*gotifyFake, string) {
	t.Helper()

	f := &gotifyFake{status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		f.mu.Lock()
		// Clone the request so the recorded copy survives past the handler
		// (r.URL/r.Header stay valid, but keep it simple and just record
		// what we need directly).
		f.requests = append(f.requests, r)
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()

		w.WriteHeader(f.status)
	}))
	t.Cleanup(srv.Close)

	return f, srv.URL
}

func (f *gotifyFake) lastRequest(t *testing.T) *http.Request {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	require.NotEmpty(t, f.requests)

	return f.requests[len(f.requests)-1]
}

func (f *gotifyFake) lastMessage(t *testing.T) gotifyMessage {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	require.NotEmpty(t, f.bodies)

	var msg gotifyMessage

	err := json.Unmarshal(f.bodies[len(f.bodies)-1], &msg)
	require.NoError(t, err)

	return msg
}

func gotifyCheck() *models.Check {
	name := "API health"

	return &models.Check{UID: "chk-1", Name: &name, Type: "http"}
}

func gotifyPayload(eventType string, settings models.JSONMap) *Payload {
	return &Payload{
		EventType: eventType,
		Incident: &models.Incident{
			UID:          "018e4a2b-incident",
			StartedAt:    time.Now().Add(-5 * time.Minute),
			FailureCount: 3,
			RelapseCount: 2,
			Details:      models.JSONMap{"failure_reason": "unexpected status code 503"},
		},
		Check:      gotifyCheck(),
		OrgSlug:    "acme",
		AppBaseURL: "https://solidping.acme.com",
		Integration: &models.Integration{
			UID: "chan-1", OrganizationUID: "org-1",
			Type:     models.ConnectionTypeGotify,
			Settings: settings,
		},
	}
}

// TestGotifySender_URLJoin asserts the request is POSTed to
// {server_url}/message, with a trailing slash on the configured server URL
// trimmed before the join.
func TestGotifySender_URLJoin(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, url := newGotifyFake(t, http.StatusOK)
	payload := gotifyPayload(eventTypeIncidentCreated, models.JSONMap{
		"server_url": url + "/", // trailing slash must be trimmed
		"app_token":  "tok-123",
	})

	sender := &GotifySender{}
	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.NoError(err)

	req := fake.lastRequest(t)
	r.Equal("/message", req.URL.Path)
	r.Empty(req.URL.RawQuery)
}

// TestGotifySender_TokenHeaderNotQueryParam asserts the app token travels in
// the X-Gotify-Key header and never leaks into the URL as a query parameter.
func TestGotifySender_TokenHeaderNotQueryParam(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, url := newGotifyFake(t, http.StatusOK)
	payload := gotifyPayload(eventTypeIncidentCreated, models.JSONMap{
		"server_url": url,
		"app_token":  "super-secret-token",
	})

	sender := &GotifySender{}
	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.NoError(err)

	req := fake.lastRequest(t)
	r.Equal("super-secret-token", req.Header.Get("X-Gotify-Key"))
	r.NotContains(req.URL.String(), "super-secret-token")
	r.NotContains(req.URL.RawQuery, "token")
}

// TestGotifySender_PriorityMapping_Created asserts a created incident uses
// the configured priority.
func TestGotifySender_PriorityMapping_Created(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, url := newGotifyFake(t, http.StatusOK)
	payload := gotifyPayload(eventTypeIncidentCreated, models.JSONMap{
		"server_url": url,
		"app_token":  "tok-123",
		"priority":   8,
	})

	sender := &GotifySender{}
	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.NoError(err)

	msg := fake.lastMessage(t)
	r.Equal(8, msg.Priority)
	r.Equal("[DOWN] API health", msg.Title)
}

// TestGotifySender_PriorityMapping_ResolvedIsAlwaysLow asserts a resolved
// incident always sends the low priority, even when a high default is
// configured — a recovery must never re-buzz a phone like a page does.
func TestGotifySender_PriorityMapping_ResolvedIsAlwaysLow(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, url := newGotifyFake(t, http.StatusOK)
	payload := gotifyPayload(eventTypeIncidentResolved, models.JSONMap{
		"server_url": url,
		"app_token":  "tok-123",
		"priority":   9,
	})

	sender := &GotifySender{}
	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.NoError(err)

	msg := fake.lastMessage(t)
	r.Equal(gotifyResolvedPriority, msg.Priority)
	r.Equal("[RECOVERED] API health", msg.Title)
}

// TestGotifySender_DefaultPriority asserts the default priority of 5 is used
// when none is configured.
func TestGotifySender_DefaultPriority(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, url := newGotifyFake(t, http.StatusOK)
	payload := gotifyPayload(eventTypeIncidentCreated, models.JSONMap{
		"server_url": url,
		"app_token":  "tok-123",
	})

	sender := &GotifySender{}
	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.NoError(err)

	msg := fake.lastMessage(t)
	r.Equal(gotifyDefaultPriority, msg.Priority)
}

// TestGotifySender_ExtrasClickURL asserts the incident URL is carried in the
// Gotify "client::notification" extras so the notification deep-links to the
// incident, mirroring how the ntfy sender enriches its messages.
func TestGotifySender_ExtrasClickURL(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, url := newGotifyFake(t, http.StatusOK)
	payload := gotifyPayload(eventTypeIncidentCreated, models.JSONMap{
		"server_url": url,
		"app_token":  "tok-123",
	})

	sender := &GotifySender{}
	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.NoError(err)

	msg := fake.lastMessage(t)
	r.NotNil(msg.Extras)

	notif, ok := msg.Extras["client::notification"].(map[string]any)
	r.True(ok)

	click, ok := notif["click"].(map[string]any)
	r.True(ok)
	r.Equal("https://solidping.acme.com/dash0/orgs/acme/incidents/018e4a2b-incident", click["url"])
}

// TestGotifySender_NonSuccessStatusErrors asserts a non-2xx response from the
// Gotify server surfaces as an error.
func TestGotifySender_NonSuccessStatusErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, url := newGotifyFake(t, http.StatusInternalServerError)
	sender := &GotifySender{}
	payload := gotifyPayload(eventTypeIncidentCreated, models.JSONMap{
		"server_url": url,
		"app_token":  "tok-123",
	})

	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.ErrorIs(err, errGotifyRequestFailed)
}

// TestGotifySender_MissingServerURLErrors asserts a missing server URL is
// rejected before any request is sent.
func TestGotifySender_MissingServerURLErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sender := &GotifySender{}
	payload := gotifyPayload(eventTypeIncidentCreated, models.JSONMap{
		"app_token": "tok-123",
	})

	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.ErrorIs(err, ErrGotifyServerURLNotConfigured)
}

// TestGotifySender_MissingAppTokenErrors asserts a missing app token is
// rejected before any request is sent.
func TestGotifySender_MissingAppTokenErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sender := &GotifySender{}
	payload := gotifyPayload(eventTypeIncidentCreated, models.JSONMap{
		"server_url": "https://gotify.example.com",
	})

	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.ErrorIs(err, ErrGotifyAppTokenNotConfigured)
}
