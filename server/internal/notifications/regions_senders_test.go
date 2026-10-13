package notifications

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// Region line in every sender (spec 2026-10-12-15). Each sender renders the
// message it would send for a multi-region payload and for a payload with no
// region; the no-region case asserts the line is absent.

const (
	regionsTestMulti = "🇪🇺 Europe West, 🇺🇸 US East (2 of 3 regions)"
	regionsTestPlain = "Europe West, US East (2 of 3 regions)"
)

func regionsTestInfo() *RegionInfo {
	return &RegionInfo{
		Failing: []string{"eu-west", "us-east"}, RegionCount: 3, FailQuorum: 2,
		Labels: map[string]RegionLabel{
			"eu-west": {Emoji: "🇪🇺", Name: "Europe West"},
			"us-east": {Emoji: "🇺🇸", Name: "US East"},
		},
	}
}

func regionsTestPayload(event string, withRegions bool) *Payload {
	name := "API health"
	slug := "api-health"
	resolvedAt := time.Now()

	payload := &Payload{
		EventType: event,
		Incident: &models.Incident{
			UID: "018e4a2b-incident", Number: 7,
			StartedAt:    time.Now().Add(-5 * time.Minute),
			ResolvedAt:   &resolvedAt,
			FailureCount: 3, RelapseCount: 1,
			Details: models.JSONMap{"failure_reason": "connection refused"},
		},
		Check:      &models.Check{UID: "chk-1", Name: &name, Slug: &slug, Type: "http", RecoveryPeriodSeconds: 60},
		OrgSlug:    "acme",
		AppBaseURL: "https://sp.acme.com",
	}

	if withRegions {
		payload.Regions = regionsTestInfo()
	}

	return payload
}

func regionsTestEmail(t *testing.T, payload *Payload) string {
	t.Helper()

	sender := &fakeEmailSender{}
	payload.Integration = &models.Integration{Settings: models.JSONMap{"to": []any{"a@acme.com"}}}
	jctx := &jobdef.JobContext{
		Services: &services.Registry{EmailSender: sender, EmailFormatter: newTestFormatter(t)},
		AppConfig: &config.Config{
			Auth:   config.AuthConfig{JWTSecret: "test-secret"},
			Server: config.ServerConfig{BaseURL: "https://sp.acme.com"},
		},
		Logger: slog.Default(),
	}

	require.NoError(t, (&EmailSender{}).Send(context.Background(), jctx, payload))
	require.Len(t, sender.sent, 1)

	return sender.sent[0].HTML + "\n" + sender.sent[0].Text
}

func regionsTestPagerDuty(t *testing.T, payload *Payload) string {
	t.Helper()

	fake, url := newPagerdutyFake(t, http.StatusAccepted)
	payload.Integration = &models.Integration{Settings: models.JSONMap{"routing_key": "key"}}

	require.NoError(t, (&PagerDutySender{EventsURL: url}).Send(context.Background(), &jobdef.JobContext{}, payload))

	raw, err := json.Marshal(fake.lastEvent(t))
	require.NoError(t, err)

	return string(raw)
}

// regionsSenders maps a sender to the message it builds for a payload.
func regionsSenders() map[string]func(t *testing.T, payload *Payload) string {
	return map[string]func(t *testing.T, payload *Payload) string{
		"slack": func(t *testing.T, p *Payload) string {
			t.Helper()

			// The resolved and reopened cards are the updates of the original
			// message; their thread replies stay one line.
			switch p.EventType {
			case eventTypeIncidentResolved:
				return mustJSON(t, (&SlackSender{}).buildResolvedUpdateMessage(p))
			case eventTypeIncidentReopened:
				return mustJSON(t, (&SlackSender{}).buildReopenedUpdateMessage(p))
			default:
				return mustJSON(t, (&SlackSender{}).buildMessage(p))
			}
		},
		"slackwebhook": func(t *testing.T, p *Payload) string {
			t.Helper()

			return mustJSON(t, (&SlackWebhookSender{}).buildMessage(p))
		},
		"discord": func(t *testing.T, p *Payload) string {
			t.Helper()

			return mustJSON(t, (&DiscordSender{}).buildEmbed(p))
		},
		"discord_resolved_update": func(t *testing.T, p *Payload) string {
			t.Helper()

			return mustJSON(t, (&DiscordSender{}).buildResolvedUpdateMessage(p))
		},
		"msteams": func(t *testing.T, p *Payload) string {
			t.Helper()

			return mustJSON(t, (&MSTeamsSender{}).buildMessage(p))
		},
		"msteamsbot": func(t *testing.T, p *Payload) string {
			t.Helper()

			return mustJSON(t, (&MSTeamsBotSender{}).buildCardActivity(p))
		},
		"googlechat": func(t *testing.T, p *Payload) string {
			t.Helper()

			return mustJSON(t, (&GoogleChatSender{}).buildMessage(p))
		},
		"mattermost": func(t *testing.T, p *Payload) string {
			t.Helper()

			return mustJSON(t, (&MattermostSender{}).buildMessage(&mattermostSettings{}, p))
		},
		"matrix": func(t *testing.T, p *Payload) string {
			t.Helper()
			plain, formatted := (&MatrixSender{}).buildContent(p)

			return plain + "\n" + formatted
		},
		"zulip": func(t *testing.T, p *Payload) string {
			t.Helper()

			return (&ZulipSender{}).buildContent(p)
		},
		"gotify": func(t *testing.T, p *Payload) string {
			t.Helper()

			return mustJSON(t, (&GotifySender{}).buildMessage(&gotifySettings{}, p))
		},
		"ntfy": func(t *testing.T, p *Payload) string {
			t.Helper()
			title, body, _, _ := (&NtfySender{}).buildContent(&ntfySettings{}, p)

			return title + "\n" + body
		},
		"pushover": func(t *testing.T, p *Payload) string {
			t.Helper()
			title, body, _, _ := (&PushoverSender{}).buildContent(&pushoverSettings{}, p)

			return title + "\n" + body
		},
		"webpush": func(t *testing.T, p *Payload) string {
			t.Helper()
			title, body := buildWebPushContent(p, "API health")

			return title + "\n" + body
		},
		"pagerduty": regionsTestPagerDuty,
		"email":     regionsTestEmail,
	}
}

func TestSendersShowTheFailingRegions(t *testing.T) {
	t.Parallel()

	// Events that carry the region line, with its wording.
	events := map[string]string{
		eventTypeIncidentCreated:   "Failing from",
		eventTypeIncidentEscalated: "Failing from",
		eventTypeIncidentReopened:  "Failing from",
		eventTypeIncidentResolved:  "Failed from",
	}

	for name, render := range regionsSenders() {
		for event, wording := range events {
			t.Run(name+"/"+event, func(t *testing.T) {
				t.Parallel()

				if name == "pagerduty" && event != eventTypeIncidentCreated && event != eventTypeIncidentReopened {
					t.Skip("PagerDuty resolves by dedup key and sends nothing on escalation")
				}

				if name == "discord_resolved_update" && event != eventTypeIncidentResolved {
					t.Skip("resolved-card update only")
				}

				if name == "msteamsbot" && event == eventTypeIncidentEscalated {
					t.Skip("same card as created")
				}

				out := render(t, regionsTestPayload(event, true))
				if name == "pagerduty" {
					// PagerDuty keeps one key rather than a sentence.
					require.Contains(t, out, `"failingFrom":"`+regionsTestMulti)

					return
				}

				require.Contains(t, out, wording)
				require.Contains(t, out, regionsTestMulti)
			})
		}
	}
}

func TestSendersOmitTheRegionLineWithoutRegions(t *testing.T) {
	t.Parallel()

	for name, render := range regionsSenders() {
		for _, event := range []string{
			eventTypeIncidentCreated, eventTypeIncidentEscalated, eventTypeIncidentReopened, eventTypeIncidentResolved,
		} {
			t.Run(name+"/"+event, func(t *testing.T) {
				t.Parallel()

				if name == "pagerduty" && event == eventTypeIncidentEscalated {
					t.Skip("PagerDuty sends nothing on escalation")
				}

				out := render(t, regionsTestPayload(event, false))
				require.NotContains(t, out, "Failing from")
				require.NotContains(t, out, "Failed from")
				require.NotContains(t, out, "failingFrom")
				require.NotContains(t, out, "regions)")
			})
		}
	}
}

func TestSendersSkipTheRegionLineOnFollowUps(t *testing.T) {
	t.Parallel()

	for name, render := range regionsSenders() {
		for _, event := range []string{eventTypeIncidentComment, eventTypeIncidentAcknowledged} {
			t.Run(name+"/"+event, func(t *testing.T) {
				t.Parallel()

				if name == "pagerduty" || name == "email" || name == "discord_resolved_update" {
					t.Skip("those senders do not model this event the same way")
				}

				payload := regionsTestPayload(event, true)
				payload.Comment = &CommentInfo{Text: "looking", AuthorName: "alice"}
				payload.Acknowledgment = &AckInfo{ActorName: "alice"}

				out := render(t, payload)
				require.NotContains(t, out, "Failing from")
				require.NotContains(t, out, "Failed from")
			})
		}
	}
}

func TestBurnAndDegradedIncidentsGetNoRegionLine(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{models.IncidentKindSLOBurn, models.IncidentKindDegraded} {
		payload := regionsTestPayload(eventTypeIncidentCreated, true)
		payload.Incident.Kind = kind

		require.Empty(t, RegionLine(payload), kind)
		fields := mustJSON(t, (&SlackSender{}).buildIncidentFields(payload, "x", ""))
		require.NotContains(t, fields, "Failing from", kind)
	}
}

func TestTwilioRegionLineHasNoEmoji(t *testing.T) {
	t.Parallel()

	for _, event := range []string{
		eventTypeIncidentCreated, eventTypeIncidentEscalated, eventTypeIncidentReopened, eventTypeIncidentResolved,
	} {
		body := (&TwilioSender{}).buildBody(nil, regionsTestPayload(event, true), &models.TwilioSettings{})
		require.Contains(t, body, regionsTestPlain, event)
		require.NotContains(t, body, "🇪🇺", event)

		for _, r := range body {
			require.Less(t, int(r), 0x2000, "GSM-friendly body only: %q in %s", r, event)
		}

		without := (&TwilioSender{}).buildBody(nil, regionsTestPayload(event, false), &models.TwilioSettings{})
		require.NotContains(t, without, "from:", event)
	}
}

func TestWebhookPayloadRegions(t *testing.T) {
	t.Parallel()

	sender := &WebhookSender{}

	multi := regionsTestPayload(eventTypeIncidentCreated, true)
	raw, err := json.Marshal(sender.buildPayload(multi))
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))

	incident := decoded["data"].(map[string]any)["incident"].(map[string]any)
	regions := incident["regions"].(map[string]any)
	require.Equal(t, []any{"eu-west", "us-east"}, regions["failingRegions"], "slugs, not labels")
	require.EqualValues(t, 3, regions["regionCount"])
	require.EqualValues(t, 2, regions["failQuorum"])

	for _, key := range []string{
		"uid", "startedAt", "resolvedAt", "durationSeconds", "title",
		"failureCount", "relapseCount", "recoveryPeriodSeconds",
	} {
		require.Contains(t, incident, key, "existing key %s is unchanged", key)
	}

	single := regionsTestPayload(eventTypeIncidentCreated, false)
	single.Regions = &RegionInfo{Failing: []string{"eu-west"}, RegionCount: 1, FailQuorum: 1}
	raw, err = json.Marshal(sender.buildPayload(single))
	require.NoError(t, err)
	require.Contains(t, string(raw), `"regions":{"failingRegions":["eu-west"],"regionCount":1,"failQuorum":1}`)

	none := regionsTestPayload(eventTypeIncidentCreated, false)
	raw, err = json.Marshal(sender.buildPayload(none))
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"regions"`)
}
