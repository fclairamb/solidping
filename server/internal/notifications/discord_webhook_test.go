package notifications

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/discord"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// discordWebhookRequest is one execute-webhook call the fake received.
type discordWebhookRequest struct {
	Query url.Values
	Body  map[string]any
}

// discordWebhookFake stands in for a Discord channel webhook. statuses are
// answered in order (the last one repeats), each with its raw body.
type discordWebhookFake struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []discordWebhookRequest
	statuses []int
	bodies   []string
}

func newDiscordWebhookFake(t *testing.T, statuses []int, bodies []string) *discordWebhookFake {
	t.Helper()

	fake := &discordWebhookFake{statuses: statuses, bodies: bodies}

	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}

		fake.mu.Lock()
		idx := len(fake.requests)
		fake.requests = append(fake.requests, discordWebhookRequest{Query: r.URL.Query(), Body: body})
		fake.mu.Unlock()

		status, respBody := http.StatusOK, `{"id":"1"}`
		if len(fake.statuses) > 0 {
			status = fake.statuses[min(idx, len(fake.statuses)-1)]
			respBody = fake.bodies[min(idx, len(fake.bodies)-1)]
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *discordWebhookFake) recorded() []discordWebhookRequest {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]discordWebhookRequest(nil), f.requests...)
}

// discordWebhookPayload builds a payload for a webhook-only integration.
func discordWebhookPayload(t *testing.T, eventType, webhookURL string) *Payload {
	t.Helper()

	payload := discordBotPayload(t, eventType)

	settingsMap, err := (&models.DiscordSettings{WebhookURL: webhookURL}).ToJSONMap()
	require.NoError(t, err)

	payload.Integration.Settings = settingsMap

	return payload
}

// TestDiscordSender_WebhookOnlyPostsEmbed pins the webhook branch of Send: an
// integration with only a webhook URL (an instance without the bot) delivers
// one embed, with no buttons and no mentions, and asks Discord to wait.
func TestDiscordSender_WebhookOnlyPostsEmbed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	hook := newDiscordWebhookFake(t, nil, nil)

	payload := discordWebhookPayload(t, eventTypeIncidentCreated, hook.server.URL+"/api/webhooks/1/tok")
	payload.OnCallMentions = []MentionTarget{{ExternalID: "999", DisplayName: "alice"}}

	// No bot token, no DB: the webhook path needs neither.
	r.NoError((&DiscordSender{}).Send(t.Context(), &jobdef.JobContext{}, payload))

	reqs := hook.recorded()
	r.Len(reqs, 1)
	r.Equal("true", reqs[0].Query.Get("wait"))

	body := reqs[0].Body
	r.Equal(productName, body["username"])
	r.NotContains(body, "components")
	r.NotContains(body, "content", "a webhook carries no on-call mention line")
	r.Equal(map[string]any{"parse": []any{}}, body["allowed_mentions"])

	embeds, ok := body["embeds"].([]any)
	r.True(ok)
	r.Len(embeds, 1)

	embed, ok := embeds[0].(map[string]any)
	r.True(ok)

	want := (&DiscordSender{}).buildEmbed(payload)
	r.Equal(want.Title, embed["title"])
	r.Contains(embed["title"], "#42")
	r.InDelta(float64(discord.ColorRed), embed["color"], 0)
	r.Equal(want.URL, embed["url"])
	r.NotEmpty(embed["fields"])
}

// TestDiscordSender_WebhookResolvedPostsResolvedEmbed covers the resolve half
// of an incident through the webhook: a standalone green embed, not a thread
// reply (a webhook has no thread to reply in).
func TestDiscordSender_WebhookResolvedPostsResolvedEmbed(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	hook := newDiscordWebhookFake(t, nil, nil)

	payload := discordWebhookPayload(t, eventTypeIncidentResolved, hook.server.URL+"/api/webhooks/1/tok")
	r.NoError((&DiscordSender{}).Send(t.Context(), &jobdef.JobContext{}, payload))

	reqs := hook.recorded()
	r.Len(reqs, 1)

	embed, ok := reqs[0].Body["embeds"].([]any)[0].(map[string]any)
	r.True(ok)
	r.Equal((&DiscordSender{}).buildEmbed(payload).Title, embed["title"])
	r.InDelta(float64(discord.ColorGreen), embed["color"], 0)
}

// TestDiscordSender_WebhookKeepsPastedQuery keeps a thread_id the operator
// pasted with the URL while adding wait=true.
func TestDiscordSender_WebhookKeepsPastedQuery(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	hook := newDiscordWebhookFake(t, nil, nil)

	payload := discordWebhookPayload(t, eventTypeIncidentCreated,
		hook.server.URL+"/api/webhooks/1/tok?thread_id=77")
	r.NoError((&DiscordSender{}).Send(t.Context(), &jobdef.JobContext{}, payload))

	reqs := hook.recorded()
	r.Len(reqs, 1)
	r.Equal("77", reqs[0].Query.Get("thread_id"))
	r.Equal("true", reqs[0].Query.Get("wait"))
}

// TestDiscordSender_BotWinsOverWebhook: an integration with both bot fields
// and a webhook URL uses the bot.
func TestDiscordSender_BotWinsOverWebhook(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	fake := newDiscordFake(t)
	hook := newDiscordWebhookFake(t, nil, nil)

	payload := discordBotPayload(t, eventTypeIncidentCreated)
	payload.Integration.Settings["webhook_url"] = hook.server.URL + "/api/webhooks/1/tok"

	r.NoError(fake.sender().Send(t.Context(), discordJCtx(&mockDBService{}, discordTestBotToken), payload))

	r.NotNil(fake.find(http.MethodPost, func(p string) bool {
		return p == "/channels/"+discordTestChannel+"/messages"
	}))
	r.Empty(hook.recorded(), "the webhook must not be used when the bot destination resolves")
}

// TestDiscordSender_NeitherBotNorWebhook returns the configuration error that
// says what to do.
func TestDiscordSender_NeitherBotNorWebhook(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	payload := discordWebhookPayload(t, eventTypeIncidentCreated, "")

	err := (&DiscordSender{}).Send(t.Context(), &jobdef.JobContext{}, payload)
	r.ErrorIs(err, ErrDiscordNoDestination)
	r.ErrorIs(err, ErrIntegrationMisconfigured)
	r.EqualError(err, "This Discord integration has no destination. Install the bot or add a webhook URL.")

	setting, ok := MissingSetting(err)
	r.True(ok)
	r.Equal("webhook URL", setting)
}

// TestDiscordSender_GuildWithoutChannelFallsBackToWebhook: a half-installed
// bot (guild, no channel) still delivers through a stored webhook URL.
func TestDiscordSender_GuildWithoutChannelFallsBackToWebhook(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	hook := newDiscordWebhookFake(t, nil, nil)

	payload := discordBotPayload(t, eventTypeIncidentCreated)
	settingsMap, err := (&models.DiscordSettings{
		GuildID: discordTestGuild, WebhookURL: hook.server.URL + "/api/webhooks/1/tok",
	}).ToJSONMap()
	r.NoError(err)

	payload.Integration.Settings = settingsMap

	r.NoError((&DiscordSender{}).Send(t.Context(), &jobdef.JobContext{}, payload))
	r.Len(hook.recorded(), 1)
}

// TestDiscordSender_WebhookRetriesOnceOn429 honors retry_after and succeeds on
// the second attempt.
func TestDiscordSender_WebhookRetriesOnceOn429(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	hook := newDiscordWebhookFake(t,
		[]int{http.StatusTooManyRequests, http.StatusOK},
		[]string{`{"message":"You are being rate limited.","retry_after":0.01,"global":false}`, `{"id":"1"}`})

	payload := discordWebhookPayload(t, eventTypeIncidentCreated, hook.server.URL+"/api/webhooks/1/tok")
	r.NoError((&DiscordSender{}).Send(t.Context(), &jobdef.JobContext{}, payload))
	r.Len(hook.recorded(), 2)
}

// TestDiscordSender_WebhookErrorClassification: a persistent 429 and a 5xx are
// retryable, a 404 (deleted webhook) and a 400 are not.
func TestDiscordSender_WebhookErrorClassification(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		status    int
		body      string
		retryable bool
		contains  string
	}{
		{"persistent 429", http.StatusTooManyRequests, `{"retry_after":0.01}`, true, "status 429"},
		{"server error", http.StatusBadGateway, `bad gateway`, true, "status 502"},
		{"deleted webhook", http.StatusNotFound, `{"message":"Unknown Webhook","code":10015}`, false, "deleted"},
		{"bad request", http.StatusBadRequest, `{"message":"Invalid Form Body"}`, false, "Invalid Form Body"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			hook := newDiscordWebhookFake(t, []int{tc.status}, []string{tc.body})

			payload := discordWebhookPayload(t, eventTypeIncidentCreated, hook.server.URL+"/api/webhooks/1/tok")
			err := (&DiscordSender{}).Send(t.Context(), &jobdef.JobContext{}, payload)
			r.Error(err)
			r.Equal(tc.retryable, jobdef.IsRetryable(err))
			r.Contains(err.Error(), tc.contains)
			r.NotErrorIs(err, ErrIntegrationMisconfigured)
		})
	}
}

func TestDiscordWebhookRetryAfter(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal(250*time.Millisecond, discordWebhookRetryAfter(http.Header{}, []byte(`{"retry_after":0.25}`)))
	r.Equal(2*time.Second, discordWebhookRetryAfter(http.Header{"Retry-After": []string{"2"}}, nil))
	r.Equal(discordWebhookRetryMax, discordWebhookRetryAfter(http.Header{}, []byte(`{"retry_after":600}`)))
	r.Equal(discordWebhookRetryFallback, discordWebhookRetryAfter(http.Header{}, []byte(`nope`)))
}
