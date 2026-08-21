package notifications

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// webhookRecorder captures what a legacy Discord webhook receives.
type webhookRecorder struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []map[string]any
}

func newWebhookRecorder(t *testing.T) *webhookRecorder {
	t.Helper()

	rec := &webhookRecorder{}

	rec.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)

		body := map[string]any{}
		_ = json.Unmarshal(raw, &body)

		rec.mu.Lock()
		rec.requests = append(rec.requests, body)
		rec.mu.Unlock()

		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(rec.server.Close)

	return rec
}

func (rec *webhookRecorder) received() []map[string]any {
	rec.mu.Lock()
	defer rec.mu.Unlock()

	out := make([]map[string]any, len(rec.requests))
	copy(out, rec.requests)

	return out
}

// legacyWebhookPayload builds a payload whose settings blob is EXACTLY what a
// pre-bot Discord integration has on disk: one key, `webhook_url`. No guild, no
// channel, no mention flag, no comment-ingestion mode.
func legacyWebhookPayload(t *testing.T, eventType, webhookURL string) *Payload {
	t.Helper()

	name := "acme api"
	resolved := time.Now()

	incident := &models.Incident{
		UID:             "inc-legacy-1",
		OrganizationUID: "org-legacy",
		StartedAt:       time.Now().Add(-3 * time.Minute),
		FailureCount:    2,
		Details:         models.JSONMap{"failure_reason": "upstream refused the connection"},
	}

	if eventType == eventTypeIncidentResolved {
		incident.ResolvedAt = &resolved
	}

	return &Payload{
		EventType: eventType,
		Incident:  incident,
		Check:     &models.Check{UID: "chk-legacy", Name: &name, Type: "http"},
		OrgSlug:   "acme",
		Integration: &models.Integration{
			UID: "conn-legacy", OrganizationUID: "org-legacy",
			Type: models.ConnectionTypeDiscord,
			// The literal historical shape — nothing else.
			Settings: models.JSONMap{"webhook_url": webhookURL},
		},
	}
}

// TestDiscordSender_LegacyWebhookStillDelivers is the regression test for the
// bot rework: an integration created before the bot existed keeps working,
// untouched, on an instance that has NO Discord bot token and NO state store.
//
// It is deliberately hostile to the bot path:
//   - jctx.AppConfig has an empty BotToken, so any code that reached for the
//     bot would fail with ErrDiscordBotNotConfigured;
//   - jctx.DBService panics on every call except the state-entry stubs, so any
//     code that started doing thread bookkeeping would blow up rather than
//     quietly pass.
//
// Every event type is exercised, because "resolved needs an existing thread" is
// a bot-mode rule that must NOT leak into the webhook path — a webhook-mode
// resolve has always posted, and still must.
func TestDiscordSender_LegacyWebhookStillDelivers(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for _, eventType := range []string{
		eventTypeIncidentCreated,
		eventTypeIncidentResolved,
		eventTypeIncidentEscalated,
		eventTypeIncidentReopened,
	} {
		rec := newWebhookRecorder(t)
		payload := legacyWebhookPayload(t, eventType, rec.server.URL)

		// No bot token anywhere on this instance.
		jctx := &jobdef.JobContext{
			DBService: &mockDBService{},
			AppConfig: &config.Config{Discord: config.DiscordOAuthConfig{}},
		}

		r.NoError((&DiscordSender{}).Send(t.Context(), jctx, payload),
			"legacy webhook delivery must not require any bot field (%s)", eventType)

		got := rec.received()
		r.Len(got, 1, "%s must produce exactly one webhook POST", eventType)
		r.Equal(productName, got[0]["username"])

		embeds, ok := got[0]["embeds"].([]any)
		r.True(ok, "%s must carry an embed", eventType)
		r.Len(embeds, 1)
	}
}

// TestDiscordSender_LegacyWebhookNeverTouchesStateStore proves the webhook path
// does no thread bookkeeping at all — the property that lets it run on an
// instance where nothing about the bot is configured.
func TestDiscordSender_LegacyWebhookNeverTouchesStateStore(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	rec := newWebhookRecorder(t)
	db := &mockDBService{}

	jctx := &jobdef.JobContext{DBService: db, AppConfig: &config.Config{}}

	r.NoError((&DiscordSender{}).Send(
		t.Context(), jctx, legacyWebhookPayload(t, eventTypeIncidentCreated, rec.server.URL)))

	r.Empty(db.getStateCalls, "webhook mode must not read incident thread state")
	r.Empty(db.setStateCalls, "webhook mode must not write incident thread state")
}

// TestDiscordSender_MissingWebhookURLStillErrors pins the historical error for
// a misconfigured legacy row.
func TestDiscordSender_MissingWebhookURLStillErrors(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	payload := legacyWebhookPayload(t, eventTypeIncidentCreated, "")

	err := (&DiscordSender{}).Send(
		t.Context(),
		&jobdef.JobContext{DBService: &mockDBService{}, AppConfig: &config.Config{}},
		payload,
	)
	r.ErrorIs(err, ErrDiscordWebhookURLNotConfigured)
}

// TestDiscordSender_GuildWithoutChannelFallsBackToWebhook covers the migration
// half-step: an org that installed the bot but has not chosen a channel yet
// keeps getting its alerts through the webhook it already had.
func TestDiscordSender_GuildWithoutChannelFallsBackToWebhook(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	rec := newWebhookRecorder(t)
	payload := legacyWebhookPayload(t, eventTypeIncidentCreated, rec.server.URL)

	settings := &models.DiscordSettings{
		WebhookURL: rec.server.URL,
		GuildID:    "G-ACME",
		GuildName:  "acme",
	}

	settingsMap, err := settings.ToJSONMap()
	r.NoError(err)
	payload.Integration.Settings = settingsMap

	r.NoError((&DiscordSender{}).Send(
		t.Context(),
		&jobdef.JobContext{DBService: &mockDBService{}, AppConfig: &config.Config{}},
		payload,
	))
	r.Len(rec.received(), 1)
}
