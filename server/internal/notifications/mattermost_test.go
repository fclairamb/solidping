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

// mattermostFake captures every request body posted by the sender and
// answers with a caller-configured status code.
type mattermostFake struct {
	mu     sync.Mutex
	bodies [][]byte
	status int
}

func newMattermostFake(t *testing.T, status int) (*mattermostFake, string) {
	t.Helper()

	f := &mattermostFake{status: status}
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

func (f *mattermostFake) lastMessage(t *testing.T) mattermostMessage {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	require.NotEmpty(t, f.bodies)

	var msg mattermostMessage

	err := json.Unmarshal(f.bodies[len(f.bodies)-1], &msg)
	require.NoError(t, err)

	return msg
}

func mattermostCheck() *models.Check {
	name := "API health"

	return &models.Check{UID: "chk-1", Name: &name, Type: "http"}
}

func mattermostPayload(settings models.JSONMap) *Payload {
	return &Payload{
		EventType: eventTypeIncidentCreated,
		Incident: &models.Incident{
			UID:          "018e4a2b-incident",
			StartedAt:    time.Now().Add(-5 * time.Minute),
			FailureCount: 3,
			RelapseCount: 2,
			Details:      models.JSONMap{"failure_reason": "unexpected status code 503"},
		},
		Check:   mattermostCheck(),
		OrgSlug: "acme",
		Integration: &models.Integration{
			UID: "chan-1", OrganizationUID: "org-1",
			Type:     models.ConnectionTypeMattermost,
			Settings: settings,
		},
	}
}

// TestMattermostSettings_DashboardFormShapedKey is the "prove the bug first"
// regression test: it builds the settings map exactly as the dash0 form does
// (integration-form.tsx's UrlPanel calls update("webhook_url", v)) and
// expects parseSettings to succeed. Before the webhookUrl -> webhook_url
// struct tag fix this failed with ErrMattermostWebhookURLNotConfigured,
// because Go's JSON unmarshaling does not fold underscores.
func TestMattermostSettings_DashboardFormShapedKey(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := mattermostPayload(models.JSONMap{
		"webhook_url": "https://mattermost.example.com/hooks/abc123",
	})

	sender := &MattermostSender{}

	settings, err := sender.parseSettings(payload)
	r.NoError(err)
	r.Equal("https://mattermost.example.com/hooks/abc123", settings.WebhookURL)
}

// TestMattermostSettings_LegacyWebhookUrlKey verifies backward compatibility:
// rows that already exist with the old camelCase "webhookUrl" key (e.g.
// created via raw API calls before the fix) must still parse.
func TestMattermostSettings_LegacyWebhookUrlKey(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := mattermostPayload(models.JSONMap{
		"webhookUrl": "https://mattermost.example.com/hooks/legacy456",
	})

	sender := &MattermostSender{}

	settings, err := sender.parseSettings(payload)
	r.NoError(err)
	r.Equal("https://mattermost.example.com/hooks/legacy456", settings.WebhookURL)
}

// TestMattermostSettings_SnakeCaseTakesPrecedence pins the fallback order:
// when both keys are present the canonical webhook_url wins.
func TestMattermostSettings_SnakeCaseTakesPrecedence(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := mattermostPayload(models.JSONMap{
		"webhook_url": "https://mattermost.example.com/hooks/canonical",
		"webhookUrl":  "https://mattermost.example.com/hooks/legacy",
	})

	sender := &MattermostSender{}

	settings, err := sender.parseSettings(payload)
	r.NoError(err)
	r.Equal("https://mattermost.example.com/hooks/canonical", settings.WebhookURL)
}

func TestMattermostSettings_MissingURLErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := mattermostPayload(models.JSONMap{})

	sender := &MattermostSender{}

	_, err := sender.parseSettings(payload)
	r.ErrorIs(err, ErrMattermostWebhookURLNotConfigured)
}

// TestMattermostSettings_IconURLKey covers the icon_url alignment: the
// optional icon override is read from the snake_case key, matching the
// key it is re-emitted under in the outgoing payload.
func TestMattermostSettings_IconURLKey(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := mattermostPayload(models.JSONMap{
		"webhook_url": "https://mattermost.example.com/hooks/abc123",
		"icon_url":    "https://example.com/icon.png",
	})

	sender := &MattermostSender{}

	settings, err := sender.parseSettings(payload)
	r.NoError(err)
	r.Equal("https://example.com/icon.png", settings.IconURL)
}

func TestMattermostSender_SendPostsExpectedPayload(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, url := newMattermostFake(t, http.StatusOK)
	payload := mattermostPayload(models.JSONMap{
		"webhook_url": url,
		"channel":     "#alerts",
		"username":    "SolidPing Bot",
		"icon_url":    "https://example.com/icon.png",
	})

	sender := &MattermostSender{}
	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.NoError(err)

	msg := fake.lastMessage(t)
	r.Equal("#alerts", msg.Channel)
	r.Equal("SolidPing Bot", msg.Username)
	r.Equal("https://example.com/icon.png", msg.IconURL)
	r.Len(msg.Attachments, 1)
	r.Equal(mattermostColorRed, msg.Attachments[0].Color)
	r.Equal(":red_circle: [DOWN] API health", msg.Attachments[0].Title)
}

func TestMattermostSender_NonSuccessStatusErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, url := newMattermostFake(t, http.StatusInternalServerError)
	sender := &MattermostSender{}
	payload := mattermostPayload(models.JSONMap{"webhook_url": url})

	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.ErrorIs(err, errMattermostWebhookFailed)
}
