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

// msTeamsFake captures every request body posted by the sender and answers
// with a caller-configured status code, mimicking a Teams Workflow endpoint
// (which typically answers 202 Accepted).
type msTeamsFake struct {
	mu     sync.Mutex
	bodies [][]byte
	status int
}

func newMSTeamsFake(t *testing.T, status int) (*msTeamsFake, string) {
	t.Helper()

	f := &msTeamsFake{status: status}
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

func (f *msTeamsFake) lastMessage(t *testing.T) msTeamsMessage {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	require.NotEmpty(t, f.bodies)

	var msg msTeamsMessage

	err := json.Unmarshal(f.bodies[len(f.bodies)-1], &msg)
	require.NoError(t, err)

	return msg
}

func msTeamsCheck() *models.Check {
	name := "API health"

	return &models.Check{UID: "chk-1", Name: &name, Type: "http"}
}

func msTeamsPayload(event string, webhookURL string) *Payload {
	return &Payload{
		EventType: event,
		Incident: &models.Incident{
			UID:          "018e4a2b-incident",
			StartedAt:    time.Now().Add(-5 * time.Minute),
			FailureCount: 3,
			RelapseCount: 2,
			Details:      models.JSONMap{"failure_reason": "connection refused"},
		},
		Check:   msTeamsCheck(),
		OrgSlug: "acme",
		Integration: &models.Integration{
			UID: "chan-1", OrganizationUID: "org-1",
			Type:     models.ConnectionTypeMSTeams,
			Settings: models.JSONMap{"webhook_url": webhookURL},
		},
	}
}

func TestMSTeamsSender_EventTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		eventType     string
		wantTitle     string
		wantColor     string
		wantFactTitle []string
	}{
		{
			name:          "created",
			eventType:     eventTypeIncidentCreated,
			wantTitle:     "[DOWN] API health",
			wantColor:     msTeamsColorAttention,
			wantFactTitle: []string{msTeamsFieldMonitor, mmFieldCause, "Failure Count"},
		},
		{
			name:          "resolved",
			eventType:     eventTypeIncidentResolved,
			wantTitle:     "[RECOVERED] API health",
			wantColor:     msTeamsColorGood,
			wantFactTitle: []string{msTeamsFieldMonitor, mmFieldDuration},
		},
		{
			name:          "escalated",
			eventType:     eventTypeIncidentEscalated,
			wantTitle:     "[ESCALATED] API health",
			wantColor:     msTeamsColorAttention,
			wantFactTitle: []string{msTeamsFieldMonitor, mmFieldCause, "Failure Count"},
		},
		{
			name:          "reopened",
			eventType:     eventTypeIncidentReopened,
			wantTitle:     "[REOPENED] API health (relapse #2)",
			wantColor:     msTeamsColorAttention,
			wantFactTitle: []string{msTeamsFieldMonitor, mmFieldCause, "Failure Count"},
		},
		{
			name:          "default/unknown",
			eventType:     "incident.unknown",
			wantTitle:     "[UPDATE] API health",
			wantColor:     "",
			wantFactTitle: []string{msTeamsFieldMonitor, mmFieldCause, "Failure Count"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			fake, url := newMSTeamsFake(t, http.StatusOK)
			payload := msTeamsPayload(tt.eventType, url)
			payload.Incident.ResolvedAt = timePtr(payload.Incident.StartedAt.Add(90 * time.Second))

			sender := &MSTeamsSender{}
			err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
			r.NoError(err)

			msg := fake.lastMessage(t)
			r.Equal("message", msg.Type)
			r.Len(msg.Attachments, 1)

			att := msg.Attachments[0]
			r.Equal("application/vnd.microsoft.card.adaptive", att.ContentType)
			r.Equal("AdaptiveCard", att.Content.Type)
			r.Equal("1.4", att.Content.Version)
			r.NotNil(att.Content.MSTeams)
			r.Equal("Full", att.Content.MSTeams.Width)
			r.Len(att.Content.Body, 2)

			titleBlock := att.Content.Body[0]
			r.Equal("TextBlock", titleBlock.Type)
			r.Equal(tt.wantTitle, titleBlock.Text)
			r.Equal("Large", titleBlock.Size)
			r.Equal("Bolder", titleBlock.Weight)
			r.Equal(tt.wantColor, titleBlock.Color)

			factSet := att.Content.Body[1]
			r.Equal("FactSet", factSet.Type)
			r.Len(factSet.Facts, len(tt.wantFactTitle))

			for i, wantTitle := range tt.wantFactTitle {
				r.Equal(wantTitle, factSet.Facts[i].Title)
			}
		})
	}
}

func TestMSTeamsSender_MissingWebhookURLErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sender := &MSTeamsSender{}
	payload := msTeamsPayload(eventTypeIncidentCreated, "")

	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.ErrorIs(err, ErrMSTeamsWebhookURLNotConfigured)
}

func TestMSTeamsSender_NonSuccessStatusErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, url := newMSTeamsFake(t, http.StatusInternalServerError)
	sender := &MSTeamsSender{}
	payload := msTeamsPayload(eventTypeIncidentCreated, url)

	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.ErrorIs(err, errMSTeamsWebhookFailed)
}

// TestMSTeamsSender_AcceptedStatusIsSuccess pins the Teams Workflow contract:
// Power Automate typically answers 202 Accepted, which must be treated as a
// success (any 2xx), not an error.
func TestMSTeamsSender_AcceptedStatusIsSuccess(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, url := newMSTeamsFake(t, http.StatusAccepted)
	sender := &MSTeamsSender{}
	payload := msTeamsPayload(eventTypeIncidentCreated, url)

	err := sender.Send(context.Background(), &jobdef.JobContext{}, payload)
	r.NoError(err)
}

// TestMSTeamsSettings_RoundTripsWebhookURLKey verifies the settings map is
// built and parsed using the exact key the dashboard form writes
// (webhook_url) — the settings key decision this spec pins down explicitly,
// unlike the pre-existing googlechat/mattermost webhook_url/webhookUrl
// mismatch (flagged separately, not replicated here).
func TestMSTeamsSettings_RoundTripsWebhookURLKey(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Build the settings map the way the dashboard integration-form.tsx
	// PerTypePanel does for msteams: `update("webhook_url", v)`.
	formSettings := models.JSONMap{
		"webhook_url": "https://example.logic.azure.com/workflows/abc",
	}

	parsed, err := models.MSTeamsSettingsFromJSONMap(formSettings)
	r.NoError(err)
	r.Equal("https://example.logic.azure.com/workflows/abc", parsed.WebhookURL)

	// And the reverse: ToJSONMap must produce the same key so a PATCH/refetch
	// round-trip is stable.
	back, err := parsed.ToJSONMap()
	r.NoError(err)
	r.Equal("https://example.logic.azure.com/workflows/abc", back["webhook_url"])
}

func timePtr(t time.Time) *time.Time {
	return &t
}
