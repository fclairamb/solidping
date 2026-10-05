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
	"github.com/fclairamb/solidping/server/internal/integrations/slack"
)

type slackWebhookFake struct {
	mu     sync.Mutex
	bodies [][]byte
	status int
}

func newSlackWebhookFake(t *testing.T, status int) (*slackWebhookFake, string) {
	t.Helper()

	f := &slackWebhookFake{status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()

		w.WriteHeader(f.status)
	}))
	t.Cleanup(srv.Close)

	return f, srv.URL
}

func (f *slackWebhookFake) last(t *testing.T) (slack.MessageResponse, string) {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	require.NotEmpty(t, f.bodies)

	raw := f.bodies[len(f.bodies)-1]

	var msg slack.MessageResponse
	require.NoError(t, json.Unmarshal(raw, &msg))

	return msg, string(raw)
}

func slackWebhookPayload(eventType string, settings models.JSONMap) *Payload {
	name := "API health"
	resolvedAt := time.Now()

	return &Payload{
		EventType: eventType,
		Incident: &models.Incident{
			UID: "018e4a2b-incident", Number: 42,
			StartedAt:    time.Now().Add(-5 * time.Minute),
			ResolvedAt:   &resolvedAt,
			FailureCount: 3,
		},
		Check:          &models.Check{UID: "chk-1", Name: &name, Type: "http"},
		OrgSlug:        "acme",
		AppBaseURL:     "https://sp.acme.com",
		Comment:        &CommentInfo{Text: "looking into it", AuthorName: "alice"},
		OnCallMentions: []MentionTarget{{DisplayName: "bob", ExternalID: "U123ABC"}},
		Integration: &models.Integration{
			UID: "chan-1", OrganizationUID: "org-1",
			Type: models.ConnectionTypeSlackWebhook, Settings: settings,
		},
	}
}

func TestSlackWebhookSender_EveryEventPostsStandaloneMessage(t *testing.T) {
	t.Parallel()

	events := []string{
		eventTypeIncidentCreated, eventTypeIncidentResolved, eventTypeIncidentEscalated,
		eventTypeIncidentReopened, eventTypeIncidentComment,
		eventTypeIncidentAcknowledged, eventTypeIncidentUnacknowledged,
	}

	for _, ev := range events {
		t.Run(ev, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			fake, url := newSlackWebhookFake(t, http.StatusOK)
			payload := slackWebhookPayload(ev, models.JSONMap{"webhook_url": url})

			// jctx has no DBService: a state lookup would panic, proving none happens.
			r.NoError((&SlackWebhookSender{}).Send(context.Background(), newJobCtx(), payload))

			msg, raw := fake.last(t)
			r.Contains(msg.Text, "API health")
			r.Contains(msg.Text, "#42")
			r.NotContains(raw, `"actions"`, "no interactive buttons")
			r.NotContains(raw, "U123ABC", "no Slack user id mentions")
			r.NotContains(raw, slackAckPrompt)
		})
	}
}

func TestSlackWebhookSender_MissingURL(t *testing.T) {
	t.Parallel()

	err := (&SlackWebhookSender{}).Send(context.Background(), newJobCtx(),
		slackWebhookPayload(eventTypeIncidentCreated, models.JSONMap{}))
	require.ErrorIs(t, err, ErrSlackWebhookURLNotConfigured)
}

func TestSlackWebhookSender_NonSuccessStatusErrors(t *testing.T) {
	t.Parallel()

	_, url := newSlackWebhookFake(t, http.StatusInternalServerError)
	err := (&SlackWebhookSender{}).Send(context.Background(), newJobCtx(),
		slackWebhookPayload(eventTypeIncidentCreated, models.JSONMap{"webhook_url": url}))
	require.ErrorIs(t, err, errSlackWebhookFailed)
}

func TestSlackWebhookSender_RefusesLoopbackTarget(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, url := newSlackWebhookFake(t, http.StatusOK)
	err := (&SlackWebhookSender{}).Send(context.Background(), loopbackJobCtx(),
		slackWebhookPayload(eventTypeIncidentCreated, models.JSONMap{"webhook_url": url}))
	r.ErrorIs(err, ErrSenderURLInvalid)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	r.Empty(fake.bodies)
}

func TestSlackWebhookSender_LegacyWebhookURLKey(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, url := newSlackWebhookFake(t, http.StatusOK)
	err := (&SlackWebhookSender{}).Send(context.Background(), newJobCtx(),
		slackWebhookPayload(eventTypeIncidentCreated, models.JSONMap{"webhookUrl": url}))
	r.NoError(err)

	msg, _ := fake.last(t)
	r.NotEmpty(msg.Text)
}
