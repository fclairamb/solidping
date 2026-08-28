package notifications

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/slack"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// unackPayload builds a representative `incident.unacknowledged` delivery. The
// incident is deliberately left with AcknowledgedAt == nil, which is the state
// the transition puts it in.
func unackPayload(connType models.ConnectionType, settings models.JSONMap, ack *AckInfo) *Payload {
	payload := ackPayload(connType, settings, ack)
	payload.EventType = eventTypeIncidentUnacknowledged
	payload.Incident.AcknowledgedAt = nil

	return payload
}

// The wording is the feature. "Unacknowledged" as a bare status word tells a
// reader nothing to do; the message has to say the incident is unowned AND
// that escalation resumes, because it does (decision 2026-08-28, option (c)).
func TestUnackWordingSaysUnownedAndThatEscalationResumes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := unackPayload(models.ConnectionTypeWebhook, nil, &AckInfo{ActorName: "alice", Via: "slack"})

	r.Equal("⚠️ Acknowledgment withdrawn: API health (#42 ·)", unackTitle(payload))
	r.Equal("Acknowledgment withdrawn by alice via Slack", unackHeadline(payload))
	r.Contains(unackPlainBody(payload), "unowned again")
	r.Contains(unackPlainBody(payload), "escalation resumes")

	// Negative control: the rejected option (a) would have promised the
	// opposite. Nothing in the copy may imply paging has stopped.
	lower := strings.ToLower(unackPlainBody(payload))
	r.NotContains(lower, "escalation stopped")
	r.NotContains(lower, "no further")
	r.NotContains(lower, "nobody else")
}

// A missing attribution must never render an empty name.
func TestUnackBodyFallsBackToANeutralActor(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	payload := unackPayload(models.ConnectionTypeWebhook, nil, nil)
	r.Contains(unackPlainBody(payload), "Acknowledgment withdrawn by Someone")
}

// The registry reuses NotifiesAcks for BOTH directions rather than adding a
// third capability flag. A channel that opted out of "someone took it" is also
// out of "they gave it back" — hearing only one half is worse than neither.
func TestAcceptsEventTypeReusesTheAckFlagForUnack(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	unack := string(models.EventTypeIncidentUnacknowledged)

	r.False(AcceptsEventType(models.ConnectionTypeTwilio, unack))
	r.True(AcceptsEventType(models.ConnectionTypeSlack, unack))
	r.True(AcceptsEventType(models.ConnectionTypeDiscord, unack))
	r.True(AcceptsEventType(models.ConnectionTypeEmail, unack))

	// The pair moves together: whatever a channel does for an ack, it does for
	// an unack. This is the property a third flag would let drift.
	for _, connType := range []models.ConnectionType{
		models.ConnectionTypeTwilio, models.ConnectionTypeSlack, models.ConnectionTypeWebhook,
		models.ConnectionTypeEmail, models.ConnectionTypePagerduty,
	} {
		r.Equal(
			AcceptsEventType(connType, string(models.EventTypeIncidentAcknowledged)),
			AcceptsEventType(connType, unack),
			"ack and unack must be gated by the same flag for %s", connType,
		)
	}

	// Positive control: the gate is ack-pair-specific, not a blanket mute.
	r.True(AcceptsEventType(models.ConnectionTypeTwilio, eventTypeIncidentCreated))
}

// PagerDuty's Events API v2 has NO un-acknowledge: event_action accepts
// trigger/acknowledge/resolve only. Sending `trigger` would either re-open a
// resolved incident or fire a fresh page at the rotation the unack is handing
// the incident back to — so the decision is to send nothing at all.
//
// This is a real trap and not a hypothetical: PagerDutySender.Send's DEFAULT
// branch is trigger, so an unhandled event type would page.
func TestPagerDutySender_UnackSendsNothing(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake, url := newPagerdutyFake(t, http.StatusAccepted)
	payload := pagerdutyPayload(eventTypeIncidentUnacknowledged)

	sender := &PagerDutySender{EventsURL: url}
	r.NoError(sender.Send(context.Background(), &jobdef.JobContext{}, payload))

	r.Equal(0, fake.requestCount(),
		"an unacknowledgment must not reach PagerDuty at all — trigger would page again")

	// Positive control: the same fake DOES record a real lifecycle event, so a
	// zero count above is the sender's decision, not a broken fixture.
	r.NoError(sender.Send(context.Background(), &jobdef.JobContext{},
		pagerdutyPayload(eventTypeIncidentCreated)))
	r.Equal(1, fake.requestCount())
}

// --- Slack: the in-place revert ---------------------------------------------

// slackCall is one request the fake Slack API received.
type slackCall struct {
	Method string
	Body   map[string]any
}

type slackFake struct {
	server *httptest.Server

	mu    sync.Mutex
	calls []slackCall
}

func newSlackFake(t *testing.T) *slackFake {
	t.Helper()

	fake := &slackFake{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}

		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &body)
		}

		fake.mu.Lock()
		fake.calls = append(fake.calls, slackCall{
			Method: strings.TrimPrefix(r.URL.Path, "/"), Body: body,
		})
		fake.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"111.222"}`))
	}))
	t.Cleanup(fake.server.Close)

	return fake
}

func (f *slackFake) find(method string) *slackCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	for i := range f.calls {
		if f.calls[i].Method == method {
			return &f.calls[i]
		}
	}

	return nil
}

func (f *slackFake) client() *slack.Client {
	return slack.NewClientWithBaseURL("xoxb-test", f.server.URL)
}

// slackThreadEntry is the stored anchor the ack rewrite and the unack revert
// both address.
func slackThreadEntry() *models.StateEntry {
	return &models.StateEntry{Value: &models.JSONMap{
		slackKeyChannelID: "C1",
		slackKeyMessageID: "1700000000.000100",
		slackKeyThreadTS:  "1700000000.000100",
	}}
}

// The highest-value half of this feature: the incident's OWN alert card is
// rewritten in place by an ack ("Acknowledged by Alice"), and an unack has to
// put it back. A thread reply alone would leave the canonical message
// asserting an ownership nobody has.
func TestSlackUnackRevertsTheAlertMessageInPlace(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := newSlackFake(t)
	payload := unackPayload(models.ConnectionTypeSlack, models.JSONMap{
		"access_token": "xoxb-test", "channel_id": "C1",
	}, &AckInfo{ActorName: "alice", Via: "web"})

	sender := &SlackSender{}
	r.NoError(sender.handleIncidentUnack(t.Context(), fake.client(), slackThreadEntry(), payload))

	update := fake.find("chat.update")
	r.NotNil(update, "the original alert message must be edited, not just replied to")
	r.Equal("1700000000.000100", update.Body["ts"],
		"the edit must target the STORED message id, i.e. the incident's own alert")
	r.Equal("C1", update.Body["channel"])

	rendered, err := json.Marshal(update.Body)
	r.NoError(err)

	text := string(rendered)
	r.Contains(text, "Acknowledgment withdrawn by alice")
	r.Contains(text, "escalation resumes")
	// The Acknowledge button comes back: the next person must be able to claim
	// the incident from the message that just told them nobody has it.
	r.Contains(text, "acknowledge_incident")
	// Negative control: the card must no longer assert the acknowledgment.
	r.NotContains(text, "Acknowledged by")

	reply := fake.find("chat.postMessage")
	r.NotNil(reply, "the retraction is also announced as a thread reply")
	r.Equal("1700000000.000100", reply.Body["thread_ts"])
}

// An unack with no recorded alert in the channel has nothing to revert and
// nothing to thread under, so it must post nothing at all rather than orphan a
// bare "acknowledgment withdrawn" and steal the incident's thread mapping.
func TestSlackUnackRequiresTheIncidentThread(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(requiresExistingThread(eventTypeIncidentUnacknowledged))

	// Positive control: an incident.created event is exactly the one that MAY
	// open a thread, so the guard is event-specific rather than blanket.
	r.False(requiresExistingThread(eventTypeIncidentCreated))
}

// --- Discord: the in-place revert -------------------------------------------

// Discord's alert card carries the same false state after an unack, and the
// message id needed to fix it was ALREADY persisted (discordKeyMessageID) — no
// storage change was required.
func TestDiscordUnackEditsTheOriginalEmbed(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fake := newDiscordFake(t)
	db := storedThreadDB(discordTestThread)

	payload := discordBotPayload(t, eventTypeIncidentUnacknowledged)
	payload.Acknowledgment = &AckInfo{ActorName: "alice", Via: "web"}

	r.NoError(fake.sender().Send(t.Context(), discordJCtx(db, discordTestBotToken), payload))

	edit := fake.find(http.MethodPatch, func(p string) bool {
		return strings.Contains(p, "/messages/"+discordTestMessage)
	})
	r.NotNil(edit, "the ORIGINAL incident embed must be edited back to unowned")

	rendered, err := json.Marshal(edit.Body)
	r.NoError(err)
	r.Contains(string(rendered), "Acknowledgment withdrawn")
	r.Contains(string(rendered), "escalation resumes")

	// The action row comes back with the edit, so the card is claimable again.
	r.Contains(string(rendered), "components")
}

func TestDiscordUnackEmbedIsRedAndNamesTheActor(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sender := &DiscordSender{}
	payload := unackPayload(models.ConnectionTypeDiscord, models.JSONMap{
		"webhook_url": "https://discord.example/hook",
	}, &AckInfo{ActorName: "bob", Via: "discord"})

	embed := sender.buildEmbed(payload)
	r.Contains(embed.Title, "Acknowledgment withdrawn")

	fields := map[string]string{}
	for _, f := range embed.Fields {
		fields[f.Name] = f.Value
	}

	r.Equal("bob", fields[fieldLabelUnacknowledgedBy])
	r.Equal("Discord", fields[fieldLabelVia])

	// An unowned open incident is as urgent as it was before anyone touched
	// it, so it must NOT keep the calmer acknowledged color.
	acked := unackPayload(models.ConnectionTypeDiscord, nil, nil)
	acked.EventType = eventTypeIncidentAcknowledged
	r.NotEqual(sender.buildEmbed(acked).Color, embed.Color)
}

// --- everything else --------------------------------------------------------

// Text-only channels must render the retraction as a sentence rather than
// falling through to the generic "[UPDATE] …" branch, which would say nothing.
func TestPlainTextSendersRenderTheUnacknowledgment(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	ack := &AckInfo{ActorName: "alice", Via: "web"}

	pushTitle, pushBody := buildWebPushContent(
		unackPayload(models.ConnectionTypeWebPush, nil, ack), "API health",
	)
	r.Contains(pushTitle, "Acknowledgment withdrawn")
	r.NotContains(pushTitle, "[UPDATE]")
	r.Contains(pushBody, "escalation resumes")

	ntfy := &NtfySender{}
	title, body, _, tags := ntfy.buildContent(
		&ntfySettings{}, unackPayload(models.ConnectionTypeNtfy, nil, ack),
	)
	r.Contains(title, "Acknowledgment withdrawn")
	r.Equal("warning", tags)
	r.Contains(body, "unowned again")

	gotify := &GotifySender{}
	gotifyMsg := gotify.buildMessage(&gotifySettings{}, unackPayload(models.ConnectionTypeGotify, nil, ack))
	r.Contains(gotifyMsg.Title, "Acknowledgment withdrawn")
	r.NotContains(gotifyMsg.Title, "[UPDATE]")

	pushover := &PushoverSender{}
	poTitle, poBody, _, _ := pushover.buildContent(
		&pushoverSettings{}, unackPayload(models.ConnectionTypePushover, nil, ack),
	)
	r.Contains(poTitle, "Acknowledgment withdrawn")
	r.Contains(poBody, "escalation resumes")
}

// The email side ships a dedicated template (otherwise the retraction would
// fall through to the ad-hoc generic body) and carries the ack magic link: an
// unowned open incident is the most actionable state there is.
func TestUnackEmailHasItsOwnTemplateAndOffersTheAckButton(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	name, ok := incidentTemplateForEvent(eventTypeIncidentUnacknowledged)
	r.True(ok)
	r.Equal("incident-unacknowledged.html", name)
	r.True(isModeledIncidentEvent(eventTypeIncidentUnacknowledged))
	r.True(canAckEvent(eventTypeIncidentUnacknowledged))

	// Negative control: a resolved incident has nothing to acknowledge.
	r.False(canAckEvent(eventTypeIncidentResolved))
}

// The webhook envelope carries the attribution as structured data, same as the
// ack, so a receiver automating on the pair sees both halves.
func TestWebhookPayloadCarriesTheUnacknowledgment(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sender := &WebhookSender{}
	payload := unackPayload(models.ConnectionTypeWebhook, nil, &AckInfo{ActorName: "alice", Via: "web"})

	out := sender.buildPayload(payload)
	r.Equal(eventTypeIncidentUnacknowledged, out.Type)
	r.NotNil(out.Data.Acknowledgment)
	r.Equal("alice", out.Data.Acknowledgment.ActorName)
}

// Guards the shared identity: one emoji per event type product-wide, and the
// retraction must not borrow the acknowledgment's checkmark.
func TestUnackEmojiIsDistinctFromAck(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.NotEqual(ackEmoji, unackEmoji)
	r.Equal("⚠️", unackEmoji)
}
