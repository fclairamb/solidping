package notifications

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// A degraded incident rides the same incident.created / incident.resolved
// events as an outage. Every sender must say it is degraded, give the pattern as
// its cause, and keep the outage's urgency (priority, color, severity) for real
// outages. Each test renders an ordinary check incident as its positive control.

const (
	degradedTestReason = "5 failures in the last 60 probes (91.7% available)"
	degradedTestTitle  = "api-health is degraded: " + degradedTestReason
	degradedTestCheck  = "API health"
	outageTestCause    = "connection refused"
)

// degradedTestPayload is the region-free sender payload, as a degraded incident
// when degraded is true, as an ordinary check incident otherwise.
func degradedTestPayload(event string, degraded bool) *Payload {
	payload := regionsTestPayload(event, false)
	payload.Incident.Kind = models.IncidentKindCheck

	if !degraded {
		return payload
	}

	title := degradedTestTitle
	payload.Incident.Kind = models.IncidentKindDegraded
	payload.Incident.Title = &title
	payload.Incident.FailureCount = 1
	payload.Incident.Details = models.JSONMap{
		"degraded_failures_fired":   true,
		"degraded_failure_count":    float64(5),
		"degraded_failure_slots":    float64(60),
		"degraded_failures":         float64(5),
		"degraded_failures_window":  float64(60),
		"degraded_availability_pct": 91.7,
		"degraded_currently_up":     true,
	}

	return payload
}

// degradedTestSenders is every sender's rendering: the region-test renderers
// plus the senders that test does not cover.
func degradedTestSenders() map[string]func(t *testing.T, payload *Payload) string {
	senders := regionsSenders()

	senders["twilio"] = func(t *testing.T, p *Payload) string {
		t.Helper()

		return (&TwilioSender{}).buildBody(nil, p, &models.TwilioSettings{})
	}
	senders["a2a"] = func(t *testing.T, p *Payload) string {
		t.Helper()

		msg := buildA2AMessage(p, "")

		return a2aMessageText(msg) + "\n" + mustJSON(t, msg)
	}
	senders["msteamsbot_followup"] = func(t *testing.T, p *Payload) string {
		t.Helper()

		return (&MSTeamsBotSender{}).followUpText(p)
	}

	return senders
}

func TestSendersRenderADegradedIncidentAsDegraded(t *testing.T) {
	t.Parallel()

	for name, render := range degradedTestSenders() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if name == "discord_resolved_update" || name == "msteamsbot_followup" {
				t.Skip("resolved / reopened follow-ups only")
			}

			r := require.New(t)

			degraded := render(t, degradedTestPayload(eventTypeIncidentCreated, true))
			r.Contains(strings.ToLower(degraded), "degraded")
			r.Contains(degraded, degradedTestReason, "the pattern is the cause")

			for _, outage := range []string{
				"[DOWN]", "is DOWN", "is down", "New incident", "Check failed", "Failure count", "rotating_light",
			} {
				r.NotContains(degraded, outage)
			}

			// Positive control: an outage still reads as one, with its own cause
			// (an SMS has room for the state only).
			outage := render(t, degradedTestPayload(eventTypeIncidentCreated, false))
			if name == "twilio" {
				r.Contains(outage, "is DOWN")
			} else {
				r.Contains(outage, outageTestCause)
			}
			r.NotContains(strings.ToLower(outage), "degraded")
		})
	}
}

func TestSendersRenderADegradedResolutionAsSteadyAgain(t *testing.T) {
	t.Parallel()

	for name, render := range degradedTestSenders() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if name == "pagerduty" {
				t.Skip("a PagerDuty resolve carries no text, only the dedup key")
			}

			r := require.New(t)

			degraded := strings.ToLower(render(t, degradedTestPayload(eventTypeIncidentResolved, true)))
			if name == "email" {
				// The mail's stylesheet names a "pill-recovered" class: read the
				// text part, which is what a human reads too.
				degraded = degraded[strings.LastIndex(degraded, "</html>"):]
			}

			r.NotContains(degraded, "recovered", "a degraded check was never down")
			r.NotContains(degraded, "back up")

			if name == "a2a" {
				// The agent gets the kind rather than a sentence.
				r.Contains(degraded, "degraded incident resolved")
			} else {
				r.Contains(degraded, "steady again")
			}

			// Positive control: an outage still recovers.
			outage := strings.ToLower(render(t, degradedTestPayload(eventTypeIncidentResolved, false)))
			r.NotContains(outage, "steady again")
			r.True(strings.Contains(outage, "recovered") || strings.Contains(outage, "resolved"), outage)
		})
	}
}

// TestDegradedTitleTags pins the title of the tag-style senders for every event
// that opens or reopens an incident, degraded and not.
func TestDegradedTitleTags(t *testing.T) {
	t.Parallel()

	titles := map[string]func(p *Payload) string{
		"ntfy": func(p *Payload) string {
			title, _, _, _ := (&NtfySender{}).buildContent(&ntfySettings{}, p)

			return title
		},
		"gotify": func(p *Payload) string {
			return (&GotifySender{}).buildMessage(&gotifySettings{}, p).Title
		},
		"pushover": func(p *Payload) string {
			title, _, _, _ := (&PushoverSender{}).buildContent(&pushoverSettings{}, p)

			return title
		},
		"matrix": func(p *Payload) string {
			title, _ := (&MatrixSender{}).titleAndLines(p)

			return title
		},
		"googlechat": func(p *Payload) string {
			return (&GoogleChatSender{}).buildMessage(p).CardsV2[0].Card.Header.Title
		},
		"msteams": func(p *Payload) string {
			title, _ := (&MSTeamsSender{}).eventTitleAndColor(p, degradedTestCheck)

			return title
		},
		"mattermost": func(p *Payload) string {
			_, title := (&MattermostSender{}).eventColorAndTitle(p, degradedTestCheck)

			return title
		},
		"webpush": func(p *Payload) string {
			title, _ := buildWebPushContent(p, degradedTestCheck)

			return title
		},
	}

	for _, tc := range []struct {
		event    string
		degraded bool
		want     string
	}{
		{eventTypeIncidentCreated, true, "[DEGRADED] " + degradedTestCheck},
		{eventTypeIncidentCreated, false, "[DOWN] " + degradedTestCheck},
		{eventTypeIncidentEscalated, true, "[ESCALATED] [DEGRADED] " + degradedTestCheck},
		{eventTypeIncidentEscalated, false, "[ESCALATED] " + degradedTestCheck},
		{eventTypeIncidentReopened, true, "[REOPENED] [DEGRADED] " + degradedTestCheck},
		{eventTypeIncidentReopened, false, "[REOPENED] " + degradedTestCheck},
		{eventTypeIncidentResolved, true, "[STEADY AGAIN] " + degradedTestCheck},
		{eventTypeIncidentResolved, false, "[RECOVERED] " + degradedTestCheck},
	} {
		for name, title := range titles {
			t.Run(name+"/"+tc.event+"/"+map[bool]string{true: "degraded", false: "check"}[tc.degraded], func(t *testing.T) {
				t.Parallel()

				got := title(degradedTestPayload(tc.event, tc.degraded))
				require.Contains(t, got, tc.want)

				if !tc.degraded {
					require.NotContains(t, got, "DEGRADED")
				}
			})
		}
	}
}

func TestDegradedResolvedTagWhenDetectionTurnedOff(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := degradedTestPayload(eventTypeIncidentResolved, true)
	payload.Incident.Details["degraded_turned_off"] = true

	title, body, _, _ := (&NtfySender{}).buildContent(&ntfySettings{}, payload)
	r.Equal("[DEGRADED DETECTION OFF] "+degradedTestCheck, title)
	r.Contains(body, "did not necessarily recover")
	r.NotContains(body, "steady again")
}

// TestDegradedIsNotUrgent pins the urgency knobs: an open degraded incident
// never gets the outage's top priority, siren, red or critical severity.
func TestDegradedIsNotUrgent(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name         string
		render       func(p *Payload) string
		wantDegraded string
		wantOutage   string
	}{
		{
			name: "ntfy priority",
			render: func(p *Payload) string {
				_, _, priority, _ := (&NtfySender{}).buildContent(&ntfySettings{}, p)

				return priority
			},
			wantDegraded: "default", wantOutage: "urgent",
		},
		{
			name: "ntfy priority overrides the configured outage priority",
			render: func(p *Payload) string {
				five := 5
				_, _, priority, _ := (&NtfySender{}).buildContent(&ntfySettings{
					Priority: &five, PriorityMapping: map[string]string{"created": "max"},
				}, p)

				return priority
			},
			wantDegraded: "default", wantOutage: "max",
		},
		{
			name: "ntfy tag",
			render: func(p *Payload) string {
				_, _, _, tags := (&NtfySender{}).buildContent(&ntfySettings{}, p)

				return tags
			},
			wantDegraded: "warning", wantOutage: "rotating_light",
		},
		{
			name: "gotify priority",
			render: func(p *Payload) string {
				return mustJSON(t, (&GotifySender{}).buildMessage(&gotifySettings{}, p).Priority)
			},
			wantDegraded: "4", wantOutage: "5",
		},
		{
			name: "gotify priority caps a high configured priority",
			render: func(p *Payload) string {
				eight := 8

				return mustJSON(t, (&GotifySender{}).buildMessage(&gotifySettings{Priority: &eight}, p).Priority)
			},
			wantDegraded: "4", wantOutage: "8",
		},
		{
			name: "gotify priority keeps a lower configured priority",
			render: func(p *Payload) string {
				two := 2

				return mustJSON(t, (&GotifySender{}).buildMessage(&gotifySettings{Priority: &two}, p).Priority)
			},
			wantDegraded: "2", wantOutage: "2",
		},
		{
			name: "pushover priority",
			render: func(p *Payload) string {
				_, _, priority, _ := (&PushoverSender{}).buildContent(&pushoverSettings{}, p)

				return mustJSON(t, priority)
			},
			wantDegraded: "0", wantOutage: "1",
		},
		{
			name: "pushover sound",
			render: func(p *Payload) string {
				_, _, _, sound := (&PushoverSender{}).buildContent(&pushoverSettings{SoundDown: "siren"}, p)

				return sound
			},
			wantDegraded: "pushover", wantOutage: "siren",
		},
		{
			name: "mattermost color",
			render: func(p *Payload) string {
				color, _ := (&MattermostSender{}).eventColorAndTitle(p, degradedTestCheck)

				return color
			},
			wantDegraded: mattermostColorOrange, wantOutage: mattermostColorRed,
		},
		{
			name: "msteams color",
			render: func(p *Payload) string {
				_, color := (&MSTeamsSender{}).eventTitleAndColor(p, degradedTestCheck)

				return color
			},
			wantDegraded: msTeamsColorWarning, wantOutage: msTeamsColorAttention,
		},
		{
			name: "msteamsbot color",
			render: func(p *Payload) string {
				_, color := (&MSTeamsBotSender{}).titleAndColor(p, degradedTestCheck)

				return color
			},
			wantDegraded: "Warning", wantOutage: "Attention",
		},
		{
			name: "discord color",
			render: func(p *Payload) string {
				return mustJSON(t, (&DiscordSender{}).buildEmbed(p).Color)
			},
			wantDegraded: "16744448", wantOutage: "16711680",
		},
		{
			name: "pagerduty severity",
			render: func(p *Payload) string {
				// The snapshot of a degraded incident often holds a DOWN probe.
				p.Incident.Details[firstResultDetailsKey] = map[string]any{"status": resultStatusStrDown}

				return pagerdutySeverity(p.Incident)
			},
			wantDegraded: pagerdutySeverityWarning, wantOutage: pagerdutySeverityCritical,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			r.Equal(tc.wantDegraded, tc.render(degradedTestPayload(eventTypeIncidentCreated, true)))
			r.Equal(tc.wantOutage, tc.render(degradedTestPayload(eventTypeIncidentCreated, false)))
		})
	}
}

func TestDegradedCauseWithoutDetailsFallsBackToTheTitle(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := degradedTestPayload(eventTypeIncidentCreated, true)
	payload.Incident.Details = nil

	r.Equal(degradedTestTitle, getFailureReason(payload.Incident))

	title, body, _, _ := (&NtfySender{}).buildContent(&ntfySettings{}, payload)
	r.Equal("[DEGRADED] "+degradedTestCheck, title)
	r.Contains(body, "Cause: "+degradedTestTitle)
	r.NotContains(body, "Check failed")

	// Positive control: an outage with no recorded cause keeps the fallback.
	outage := degradedTestPayload(eventTypeIncidentCreated, false)
	outage.Incident.Details = nil
	r.Equal(defaultFailureReason, getFailureReason(outage.Incident))
}

func TestWebhookAndPagerDutyExposeTheIncidentKind(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		degraded bool
		kind     string
	}{
		{true, models.IncidentKindDegraded},
		{false, models.IncidentKindCheck},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			raw, err := json.Marshal((&WebhookSender{}).buildPayload(degradedTestPayload(eventTypeIncidentCreated, tc.degraded)))
			r.NoError(err)

			var decoded struct {
				Data struct {
					Incident map[string]any `json:"incident"`
				} `json:"data"`
			}
			r.NoError(json.Unmarshal(raw, &decoded))
			r.Equal(tc.kind, decoded.Data.Incident["kind"])

			for _, key := range []string{"uid", "startedAt", "title", "failureCount", "relapseCount"} {
				r.Contains(decoded.Data.Incident, key, "existing key %s is unchanged", key)
			}

			event := regionsTestPagerDuty(t, degradedTestPayload(eventTypeIncidentCreated, tc.degraded))
			r.Contains(event, `"incidentKind":"`+tc.kind+`"`)
		})
	}
}
