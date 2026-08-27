package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// zulipFake captures every request posted by the sender (headers and raw
// body) and answers with a caller-configured status code and body. It also
// rejects a non-form-encoded request outright — the same trap the Slack list
// APIs sprung (a too-lenient fake silently accepting a JSON body that the
// real API would ignore) must not repeat here.
type zulipFake struct {
	mu           sync.Mutex
	requests     []*http.Request
	bodies       [][]byte
	status       int
	responseBody []byte // defaults to a Zulip-shaped success body when nil
}

func newZulipFake(t *testing.T, status int) (*zulipFake, string) {
	t.Helper()

	f := &zulipFake{status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		contentType := r.Header.Get("Content-Type")

		f.mu.Lock()
		f.requests = append(f.requests, r)
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()

		if !strings.HasPrefix(contentType, "application/x-www-form-urlencoded") {
			// Zulip's real /api/v1/messages endpoint expects a form-encoded
			// body; a JSON body would not be parsed the way the sender
			// intends. The fake must fail loudly here rather than leniently
			// parsing JSON as if it were form data, or a regression back to
			// json.Marshal would pass this suite unnoticed.
			w.WriteHeader(http.StatusUnsupportedMediaType)
			_, _ = w.Write([]byte(`{"result":"error","msg":"expected form-encoded body, got ` + contentType + `"}`))

			return
		}

		w.WriteHeader(f.status)

		if f.responseBody != nil {
			_, _ = w.Write(f.responseBody)

			return
		}

		_, _ = w.Write([]byte(`{"result":"success","msg":"","id":1}`))
	}))
	t.Cleanup(srv.Close)

	return f, srv.URL
}

func (f *zulipFake) lastRequest(t *testing.T) *http.Request {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	require.NotEmpty(t, f.requests)

	return f.requests[len(f.requests)-1]
}

func (f *zulipFake) lastForm(t *testing.T) url.Values {
	t.Helper()

	f.mu.Lock()
	body := f.bodies[len(f.bodies)-1]
	f.mu.Unlock()

	form, err := url.ParseQuery(string(body))
	require.NoError(t, err)

	return form
}

func zulipCheck(name string) *models.Check {
	return &models.Check{UID: "chk-1", Name: &name, Type: "http"}
}

func zulipPayload(eventType string, incidentNumber int64, settings models.JSONMap) *Payload {
	return &Payload{
		EventType: eventType,
		Incident: &models.Incident{
			UID:          "018e4a2b-incident",
			Number:       incidentNumber,
			StartedAt:    time.Now().Add(-5 * time.Minute),
			FailureCount: 3,
			RelapseCount: 2,
			Details:      models.JSONMap{"failure_reason": "unexpected status code 503"},
		},
		Check:      zulipCheck("API health"),
		OrgSlug:    "acme",
		AppBaseURL: "https://solidping.acme.com",
		Integration: &models.Integration{
			UID: "chan-1", OrganizationUID: "org-1",
			Type:     models.ConnectionTypeZulip,
			Settings: settings,
		},
	}
}

func zulipSettingsMap(siteURL string) models.JSONMap {
	return models.JSONMap{
		"site_url":  siteURL,
		"bot_email": "solidping-bot@acme.com",
		"api_key":   "super-secret-api-key",
		"stream":    "alerts",
	}
}

// TestZulipFake_RejectsJSONBody proves the fake itself would reject a JSON
// body from a hypothetical regression back to json.Marshal, so the guarantee
// isn't merely "the sender happens to send form data today" but "a sender
// that stopped doing so would fail this suite".
func TestZulipFake_RejectsJSONBody(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, addr := newZulipFake(t, http.StatusOK)

	body, err := json.Marshal(map[string]string{
		"type": "stream", "to": "alerts", "topic": "x", "content": "y",
	})
	r.NoError(err)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, addr, bytes.NewReader(body))
	r.NoError(err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)

	defer func() { _ = resp.Body.Close() }()

	r.NotEqual(http.StatusOK, resp.StatusCode)
}

// TestZulipSender_SendsFormEncodedWithBasicAuth asserts the real sender
// posts a form-encoded body (so it passes the JSON-rejecting fake above) and
// authenticates with HTTP Basic auth using bot_email:api_key.
func TestZulipSender_SendsFormEncodedWithBasicAuth(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, addr := newZulipFake(t, http.StatusOK)
	payload := zulipPayload(eventTypeIncidentCreated, 42, zulipSettingsMap(addr))

	sender := &ZulipSender{}
	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.NoError(err)

	req := fake.lastRequest(t)
	r.Equal("/api/v1/messages", req.URL.Path)
	r.True(strings.HasPrefix(req.Header.Get("Content-Type"), "application/x-www-form-urlencoded"))

	user, pass, ok := req.BasicAuth()
	r.True(ok)
	r.Equal("solidping-bot@acme.com", user)
	r.Equal("super-secret-api-key", pass)
}

// TestZulipSender_StreamAndTopicRouting asserts the stream and topic form
// fields are populated from settings and the incident.
func TestZulipSender_StreamAndTopicRouting(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, addr := newZulipFake(t, http.StatusOK)
	payload := zulipPayload(eventTypeIncidentCreated, 42, zulipSettingsMap(addr))

	sender := &ZulipSender{}
	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.NoError(err)

	form := fake.lastForm(t)
	r.Equal("stream", form.Get("type"))
	r.Equal("alerts", form.Get("to"))
	r.Equal("API health (#42)", form.Get("topic"))
	r.Contains(form.Get("content"), "API health")
}

// TestZulipSender_SameIncidentProducesIdenticalTopicAcrossEvents is the one
// real design point of the spec: created / escalated / comment /
// acknowledged / resolved events for the same incident must derive the
// identical topic string, purely from the payload, so Zulip threads the
// whole incident into one topic automatically.
func TestZulipSender_SameIncidentProducesIdenticalTopicAcrossEvents(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, addr := newZulipFake(t, http.StatusOK)
	settings := zulipSettingsMap(addr)
	sender := &ZulipSender{}

	events := []string{
		eventTypeIncidentCreated,
		eventTypeIncidentEscalated,
		eventTypeIncidentComment,
		eventTypeIncidentAcknowledged,
		eventTypeIncidentResolved,
	}

	topics := make([]string, 0, len(events))

	for _, eventType := range events {
		payload := zulipPayload(eventType, 42, settings)
		if eventType == eventTypeIncidentComment {
			payload.Comment = &CommentInfo{Text: "still investigating", AuthorName: "alice"}
		}

		if eventType == eventTypeIncidentAcknowledged {
			payload.Acknowledgment = &AckInfo{ActorName: "bob", Via: "web"}
		}

		err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
		r.NoError(err)

		topics = append(topics, fake.lastForm(t).Get("topic"))
	}

	for i, topic := range topics {
		r.Equalf(topics[0], topic, "event %s produced a different topic", events[i])
	}
}

// TestZulipTopic_TruncatesTo60Chars asserts a topic longer than Zulip's
// 60-character limit is truncated deterministically, purely from the
// payload — and that the incident-ref suffix survives the truncation. The
// suffix is the only part of the topic that distinguishes one incident from
// another on the same check, so a truncation that drops it would defeat the
// point of having a topic per incident at all.
func TestZulipTopic_TruncatesTo60Chars(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	longName := strings.Repeat("a very long check name indeed ", 5) // well over 60 chars
	payload := zulipPayload(eventTypeIncidentCreated, 42, zulipSettingsMap("https://acme.zulipchat.com"))
	payload.Check = zulipCheck(longName)

	topic := zulipTopic(payload)
	r.LessOrEqual(len([]rune(topic)), zulipTopicMaxLen)
	r.True(strings.HasPrefix(topic, longName[:20]))
	r.True(strings.HasSuffix(topic, " (#42)"), "topic must keep the incident ref, got %q", topic)
}

// TestZulipTopic_DifferentIncidentsProduceDifferentTopics_OrdinaryName is the
// positive control for topic derivation: two different incidents on an
// ordinary-length check name must never produce the same topic, regardless
// of check name length.
func TestZulipTopic_DifferentIncidentsProduceDifferentTopics_OrdinaryName(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	settings := zulipSettingsMap("https://acme.zulipchat.com")
	payloadA := zulipPayload(eventTypeIncidentCreated, 42, settings)
	payloadB := zulipPayload(eventTypeIncidentCreated, 99, settings)

	r.NotEqual(zulipTopic(payloadA), zulipTopic(payloadB))
}

// TestZulipTopic_DifferentIncidentsProduceDifferentTopics_OverlongName is the
// regression test for the truncation bug this test suite originally missed:
// naively truncating the whole "<name> (#<N>)" string from the right eats
// the incident-ref suffix first once the check name alone is long enough,
// which collapses every incident on that check into one identical topic —
// the opposite of "one thread per incident". composeZulipTopic budgets the
// suffix before truncating the name, so this must diverge just like the
// ordinary-length case above.
func TestZulipTopic_DifferentIncidentsProduceDifferentTopics_OverlongName(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	longName := strings.Repeat("a very long check name indeed ", 5) // well over 60 chars
	settings := zulipSettingsMap("https://acme.zulipchat.com")

	payloadA := zulipPayload(eventTypeIncidentCreated, 42, settings)
	payloadA.Check = zulipCheck(longName)

	payloadB := zulipPayload(eventTypeIncidentCreated, 99, settings)
	payloadB.Check = zulipCheck(longName)

	r.NotEqual(zulipTopic(payloadA), zulipTopic(payloadB))
}

// TestZulipTopic_FallsBackToCheckNameWithoutIncidentNumber asserts the topic
// derivation tolerates an incident with no short reference yet (created
// before the numbering existed and never backfilled).
func TestZulipTopic_FallsBackToCheckNameWithoutIncidentNumber(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := zulipPayload(eventTypeIncidentCreated, 0, zulipSettingsMap("https://acme.zulipchat.com"))

	r.Equal("API health", zulipTopic(payload))
}

// TestZulipSender_NonSuccessStatusErrors asserts an HTTP-level failure (a
// non-2xx status) surfaces as an error.
func TestZulipSender_NonSuccessStatusErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, addr := newZulipFake(t, http.StatusInternalServerError)
	sender := &ZulipSender{}
	payload := zulipPayload(eventTypeIncidentCreated, 42, zulipSettingsMap(addr))

	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.ErrorIs(err, errZulipRequestFailed)
}

// TestZulipSender_ApplicationErrorInHTTP200Errors asserts Zulip's
// application-level failure shape — HTTP 200 with a JSON body carrying
// "result": "error" — is treated as a failure too. Zulip reports invalid
// stream/topic/auth combinations exactly this way instead of a 4xx, so a
// sender that only checks the status code would silently swallow the error.
func TestZulipSender_ApplicationErrorInHTTP200Errors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, addr := newZulipFake(t, http.StatusOK)
	fake.responseBody = []byte(`{"result":"error","msg":"Invalid stream name 'alerts'"}`)

	sender := &ZulipSender{}
	payload := zulipPayload(eventTypeIncidentCreated, 42, zulipSettingsMap(addr))

	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.ErrorIs(err, errZulipRequestFailed)
	r.Contains(err.Error(), "Invalid stream name")
}

// TestZulipSender_MissingSiteURLErrors asserts a missing site URL is
// rejected before any request is sent.
func TestZulipSender_MissingSiteURLErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sender := &ZulipSender{}
	settings := zulipSettingsMap("")
	delete(settings, "site_url")
	payload := zulipPayload(eventTypeIncidentCreated, 42, settings)

	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.ErrorIs(err, ErrZulipSiteURLNotConfigured)
}

// TestZulipSender_MissingBotEmailErrors asserts a missing bot email is
// rejected before any request is sent.
func TestZulipSender_MissingBotEmailErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sender := &ZulipSender{}
	settings := zulipSettingsMap("https://acme.zulipchat.com")
	delete(settings, "bot_email")
	payload := zulipPayload(eventTypeIncidentCreated, 42, settings)

	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.ErrorIs(err, ErrZulipBotEmailNotConfigured)
}

// TestZulipSender_MissingAPIKeyErrors asserts a missing API key is rejected
// before any request is sent.
func TestZulipSender_MissingAPIKeyErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sender := &ZulipSender{}
	settings := zulipSettingsMap("https://acme.zulipchat.com")
	delete(settings, "api_key")
	payload := zulipPayload(eventTypeIncidentCreated, 42, settings)

	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.ErrorIs(err, ErrZulipAPIKeyNotConfigured)
}

// TestZulipSender_MissingStreamErrors asserts a missing stream is rejected
// before any request is sent.
func TestZulipSender_MissingStreamErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sender := &ZulipSender{}
	settings := zulipSettingsMap("https://acme.zulipchat.com")
	delete(settings, "stream")
	payload := zulipPayload(eventTypeIncidentCreated, 42, settings)

	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.ErrorIs(err, ErrZulipStreamNotConfigured)
}
