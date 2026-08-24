package notifications

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// ackPayload builds a representative `incident.acknowledged` delivery for a
// channel of the given type.
func ackPayload(connType models.ConnectionType, settings models.JSONMap, ack *AckInfo) *Payload {
	name := "API health"
	slug := "api-health"
	ackedAt := time.Now().Add(-time.Minute)

	return &Payload{
		EventType: eventTypeIncidentAcknowledged,
		Incident: &models.Incident{
			UID:             "018e4a2b-incident",
			Number:          42,
			OrganizationUID: "org-1",
			StartedAt:       time.Now().Add(-10 * time.Minute),
			AcknowledgedAt:  &ackedAt,
			FailureCount:    3,
		},
		Check:          &models.Check{UID: "chk-1", Name: &name, Slug: &slug, Type: "http"},
		OrgSlug:        "acme",
		AppBaseURL:     "https://solidping.example",
		Integration:    &models.Integration{UID: "chan-1", OrganizationUID: "org-1", Type: connType, Settings: settings},
		Acknowledgment: ack,
	}
}

func TestAckTitleAndBodyNameTheActorAndChannel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := ackPayload(models.ConnectionTypeWebhook, nil, &AckInfo{ActorName: "alice", Via: "slack"})

	r.Equal("✅ Acknowledged: API health (#42 ·)", ackTitle(payload))
	r.Equal("alice acknowledged this incident via Slack.", ackPlainBody(payload))
	r.Equal("alice acknowledged this incident via Slack", ackSentence(payload))
}

// A dashboard ack renders no "via" clause: "via web" is noise, and the same
// omission keeps the sentence readable when the channel is unknown entirely.
func TestAckBodyOmitsTheChannelForWebAndUnknown(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, via := range []string{"web", "", "something-new"} {
		payload := ackPayload(models.ConnectionTypeWebhook, nil, &AckInfo{ActorName: "alice", Via: via})
		r.Equal("alice acknowledged this incident.", ackPlainBody(payload), "via=%q", via)
	}
}

// A missing attribution must never render an empty name — a channel message
// reading "  acknowledged this incident" looks like a bug.
func TestAckBodyFallsBackToANeutralActor(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Equal("Someone", ackActor(nil))
	r.Equal("Someone", ackActor(&AckInfo{ActorName: "   "}))

	payload := ackPayload(models.ConnectionTypeWebhook, nil, nil)
	r.Equal("Someone acknowledged this incident.", ackPlainBody(payload))
}

// PagerDuty is the one channel that can express an acknowledgment natively,
// and the one where getting it wrong is destructive: its default branch is
// `trigger`, which reuses the dedup_key and would RE-OPEN a resolved incident.
func TestPagerDutySender_AcknowledgeUsesTheNativeAction(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, url := newPagerdutyFake(t, http.StatusAccepted)
	payload := pagerdutyPayload(eventTypeIncidentAcknowledged)

	sender := &PagerDutySender{EventsURL: url}
	r.NoError(sender.Send(context.Background(), &jobdef.JobContext{}, payload))

	r.Equal(1, fake.requestCount())

	event := fake.lastEvent(t)
	r.Equal(pagerdutyEventActionAcknowledge, event["event_action"])
	r.NotEqual(pagerdutyEventActionTrigger, event["event_action"],
		"an acknowledgment must never re-trigger the PagerDuty incident")
	r.Equal(payload.Incident.UID, event["dedup_key"],
		"the acknowledgment must correlate to the incident SolidPing already opened")
}

// Slack posts the notice as a THREAD REPLY under the incident's alert, never as
// an edit of the alert itself: the check is still down, and a channel that
// turns its red message green on an ack teaches people to ignore red.
func TestSlackAckIsAThreadReplyThatNamesTheActor(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sender := &SlackSender{}
	payload := ackPayload(models.ConnectionTypeSlack, models.JSONMap{
		"access_token": "xoxb-test", "channel_id": "C1",
	}, &AckInfo{ActorName: "alice", Via: "slack"})

	msg := sender.buildMessage(payload)
	r.Contains(msg.Text, "alice acknowledged this incident via Slack")
	r.Contains(msg.Text, "#42")
	r.Contains(msg.Text, "the incident is still open")

	r.True(requiresExistingThread(eventTypeIncidentAcknowledged),
		"a bare ack with no alert in the channel would be a context-free orphan")
}

func TestDiscordAckEmbedIsBlueAndNamesTheActor(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sender := &DiscordSender{}
	payload := ackPayload(models.ConnectionTypeDiscord, models.JSONMap{
		"webhook_url": "https://discord.example/hook",
	}, &AckInfo{ActorName: "bob", Via: "discord"})

	embed := sender.buildEmbed(payload)
	r.Contains(embed.Title, "Acknowledged")
	r.Contains(embed.Description, "bob acknowledged this incident via Discord")

	fields := map[string]string{}
	for _, f := range embed.Fields {
		fields[f.Name] = f.Value
	}

	r.Equal("bob", fields[fieldLabelAcknowledgedBy])
	r.Equal("Discord", fields[fieldLabelVia])

	// Green is the recovery color; an acknowledged incident is still down.
	resolved := ackPayload(models.ConnectionTypeDiscord, nil, nil)
	resolved.EventType = eventTypeIncidentResolved
	r.NotEqual(sender.buildEmbed(resolved).Color, embed.Color)
}

// Text-only channels must render the acknowledgment as a sentence, not fall
// through to the generic "[UPDATE] …" branch that says nothing useful.
func TestPlainTextSendersRenderTheAcknowledgment(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ack := &AckInfo{ActorName: "alice", Via: "phone"}

	pushTitle, pushBody := buildWebPushContent(
		ackPayload(models.ConnectionTypeWebPush, nil, ack), "API health",
	)
	r.Contains(pushTitle, "Acknowledged")
	r.NotContains(pushTitle, "[UPDATE]")
	r.Contains(pushBody, "alice acknowledged this incident via phone")

	ntfy := &NtfySender{}
	title, body, _, tags := ntfy.buildContent(
		&ntfySettings{}, ackPayload(models.ConnectionTypeNtfy, nil, ack),
	)
	r.Contains(title, "Acknowledged")
	r.Equal("white_check_mark", tags)
	r.Contains(body, "alice acknowledged this incident")
}

// The registry decides which channels opt out. SMS/voice must not be billed
// for an acknowledgment; every other channel receives it.
func TestAcceptsEventTypeGatesAcksOnPagingCostChannels(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ack := string(models.EventTypeIncidentAcknowledged)

	r.False(AcceptsEventType(models.ConnectionTypeTwilio, ack))
	r.True(AcceptsEventType(models.ConnectionTypeSlack, ack))
	r.True(AcceptsEventType(models.ConnectionTypeWebhook, ack))
	r.True(AcceptsEventType(models.ConnectionTypeEmail, ack))

	// Positive control: the gate is ack-specific, not a blanket Twilio mute.
	r.True(AcceptsEventType(models.ConnectionTypeTwilio, eventTypeIncidentCreated))
}

// The webhook envelope carries the attribution as structured data — a receiver
// automating on acknowledgments needs the actor, not a rendered sentence.
func TestWebhookPayloadCarriesTheAcknowledgment(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sender := &WebhookSender{}
	payload := ackPayload(models.ConnectionTypeWebhook, nil, &AckInfo{ActorName: "alice", Via: "slack"})

	out := sender.buildPayload(payload)
	r.Equal(eventTypeIncidentAcknowledged, out.Type)
	r.NotNil(out.Data.Acknowledgment)
	r.Equal("alice", out.Data.Acknowledgment.ActorName)
	r.Equal("slack", out.Data.Acknowledgment.Via)
	r.NotNil(out.Data.Incident.AcknowledgedAt)

	// Negative control: an unrelated event must stay byte-identical to before,
	// so existing receivers see no new keys.
	other := ackPayload(models.ConnectionTypeWebhook, nil, nil)
	other.EventType = eventTypeIncidentCreated
	other.Incident.AcknowledgedAt = nil
	r.Nil(sender.buildPayload(other).Data.Acknowledgment)
}

// The email path needs its own template — without one it falls through to the
// ad-hoc "incident update" body, which names neither the actor nor the state.
func TestEmailAcknowledgedTemplateIsRegistered(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	name, ok := incidentTemplateForEvent(eventTypeIncidentAcknowledged)
	r.True(ok)
	r.Equal("incident-acknowledged.html", name)
	r.True(isModeledIncidentEvent(eventTypeIncidentAcknowledged))

	// An acknowledged incident has already been claimed, so its email must not
	// offer another magic-link ack button.
	r.False(canAckEvent(eventTypeIncidentAcknowledged))
}
