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

// googleChatFake captures every request posted by the sender and answers with
// a caller-configured status code.
type googleChatFake struct {
	mu     sync.Mutex
	urls   []string
	bodies [][]byte
	status int
}

func newGoogleChatFake(t *testing.T, status int) (*googleChatFake, string) {
	t.Helper()

	f := &googleChatFake{status: status}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		f.mu.Lock()
		f.urls = append(f.urls, r.URL.String())
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()

		w.WriteHeader(f.status)
	}))
	t.Cleanup(srv.Close)

	return f, srv.URL
}

func (f *googleChatFake) lastMessage(t *testing.T) googleChatMessage {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	require.NotEmpty(t, f.bodies)

	var msg googleChatMessage

	err := json.Unmarshal(f.bodies[len(f.bodies)-1], &msg)
	require.NoError(t, err)

	return msg
}

func (f *googleChatFake) lastURL(t *testing.T) string {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	require.NotEmpty(t, f.urls)

	return f.urls[len(f.urls)-1]
}

func googleChatCheck() *models.Check {
	name := "API health"

	return &models.Check{UID: "chk-1", Name: &name, Type: "http"}
}

func googleChatPayload(event string, settings models.JSONMap) *Payload {
	return &Payload{
		EventType: event,
		Incident: &models.Incident{
			UID:          "018e4a2b-incident",
			StartedAt:    time.Now().Add(-5 * time.Minute),
			FailureCount: 3,
			RelapseCount: 2,
			Details:      models.JSONMap{"failure_reason": "connection refused"},
		},
		Check:   googleChatCheck(),
		OrgSlug: "acme",
		Integration: &models.Integration{
			UID: "chan-1", OrganizationUID: "org-1",
			Type:     models.ConnectionTypeGoogleChat,
			Settings: settings,
		},
	}
}

// TestGoogleChatSettings_DashboardFormShapedKey is the "prove the bug first"
// regression test: it builds the settings map exactly as the dash0 form does
// (integration-form.tsx's UrlPanel calls update("webhook_url", v)) and
// expects parseSettings to succeed. Before the webhookUrl -> webhook_url
// struct tag fix this failed with ErrGoogleChatWebhookURLNotConfigured,
// because Go's JSON unmarshaling does not fold underscores.
func TestGoogleChatSettings_DashboardFormShapedKey(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := googleChatPayload(eventTypeIncidentCreated, models.JSONMap{
		"webhook_url": "https://chat.googleapis.com/v1/spaces/AAA/messages?key=k&token=t",
	})

	sender := &GoogleChatSender{}

	settings, err := sender.parseSettings(payload)
	r.NoError(err)
	r.Equal("https://chat.googleapis.com/v1/spaces/AAA/messages?key=k&token=t", settings.WebhookURL)
}

// TestGoogleChatSettings_LegacyWebhookUrlKey verifies backward compatibility:
// rows that already exist with the old camelCase "webhookUrl" key (e.g.
// created via raw API calls before the fix) must still parse.
func TestGoogleChatSettings_LegacyWebhookUrlKey(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := googleChatPayload(eventTypeIncidentCreated, models.JSONMap{
		"webhookUrl": "https://chat.googleapis.com/v1/spaces/BBB/messages?key=k&token=t",
	})

	sender := &GoogleChatSender{}

	settings, err := sender.parseSettings(payload)
	r.NoError(err)
	r.Equal("https://chat.googleapis.com/v1/spaces/BBB/messages?key=k&token=t", settings.WebhookURL)
}

// TestGoogleChatSettings_SnakeCaseTakesPrecedence pins the fallback order:
// when both keys are present (shouldn't normally happen, but is possible on
// a hand-edited row) the canonical webhook_url wins.
func TestGoogleChatSettings_SnakeCaseTakesPrecedence(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := googleChatPayload(eventTypeIncidentCreated, models.JSONMap{
		"webhook_url": "https://chat.googleapis.com/v1/spaces/CANONICAL/messages",
		"webhookUrl":  "https://chat.googleapis.com/v1/spaces/LEGACY/messages",
	})

	sender := &GoogleChatSender{}

	settings, err := sender.parseSettings(payload)
	r.NoError(err)
	r.Equal("https://chat.googleapis.com/v1/spaces/CANONICAL/messages", settings.WebhookURL)
}

func TestGoogleChatSettings_MissingURLErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := googleChatPayload(eventTypeIncidentCreated, models.JSONMap{})

	sender := &GoogleChatSender{}

	_, err := sender.parseSettings(payload)
	r.ErrorIs(err, ErrGoogleChatWebhookURLNotConfigured)
}

func TestGoogleChatSender_SendPostsExpectedPayload(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, url := newGoogleChatFake(t, http.StatusOK)
	// Real Google Chat webhook URLs always carry a query string
	// (?key=...&token=...), which is what lets buildURL append
	// "&threadKey=..." safely; mimic that shape here.
	payload := googleChatPayload(eventTypeIncidentCreated, models.JSONMap{
		"webhook_url": url + "?key=k&token=t",
	})

	sender := &GoogleChatSender{}
	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.NoError(err)

	// threadKeyEnabled defaults to true when absent, so the request URL must
	// carry the threadKey/messageReplyOption query parameters appended by
	// buildURL.
	r.Contains(fake.lastURL(t), "threadKey=018e4a2b-incident")

	msg := fake.lastMessage(t)
	r.Len(msg.CardsV2, 1)
	r.Equal("incident-018e4a2b-incident", msg.CardsV2[0].CardID)
	r.Equal("[DOWN] API health", msg.CardsV2[0].Card.Header.Title)
}

func TestGoogleChatSender_NonSuccessStatusErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, url := newGoogleChatFake(t, http.StatusInternalServerError)
	sender := &GoogleChatSender{}
	payload := googleChatPayload(eventTypeIncidentCreated, models.JSONMap{"webhook_url": url + "?key=k&token=t"})

	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.ErrorIs(err, errGoogleChatWebhookFailed)
}

// TestDiscordSettings_DashboardFormShapedKey is a positive control: the same
// dashboard-shaped settings map, run through the already-correct Discord
// sender, must parse. This proves the test file is exercising the general
// form -> sender settings-key contract, not just asserting that the two
// specifically-patched types (Google Chat, Mattermost) now happen to work.
func TestDiscordSettings_DashboardFormShapedKey(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := &Payload{
		EventType: eventTypeIncidentCreated,
		Incident: &models.Incident{
			UID:       "018e4a2b-incident",
			StartedAt: time.Now().Add(-5 * time.Minute),
		},
		Check: googleChatCheck(),
		Integration: &models.Integration{
			UID: "chan-1", OrganizationUID: "org-1",
			Type: models.ConnectionTypeDiscord,
			Settings: models.JSONMap{
				"webhook_url": "https://discord.com/api/webhooks/123/abc",
			},
		},
	}

	sender := &DiscordSender{}

	settings, err := sender.parseSettings(payload)
	r.NoError(err)
	r.Equal("https://discord.com/api/webhooks/123/abc", settings.WebhookURL)
}
