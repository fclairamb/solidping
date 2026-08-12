package notifications

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// mentionPayload builds a minimal payload for one event type with the given
// resolved mention targets.
func mentionPayload(eventType string, targets []MentionTarget) *Payload {
	name := "API Health"

	return &Payload{
		EventType:      eventType,
		Incident:       &models.Incident{UID: "inc-1", FailureCount: 3},
		Check:          &models.Check{Name: &name},
		OnCallMentions: targets,
	}
}

// messageText concatenates the top-level text and every block text of a Slack
// message, which is exactly the surface a human (or Slack's notifier) reads.
func messageText(msg *slackMessageForTest) string { return msg.all }

// slackMessageForTest is a flattened Slack message.
type slackMessageForTest struct{ all string }

// flatten renders a built Slack message down to one searchable string.
func flatten(t *testing.T, sender *SlackSender, payload *Payload) *slackMessageForTest {
	t.Helper()

	msg := sender.buildMessage(payload)
	require.NotNil(t, msg)

	out := msg.Text

	for _, attachment := range msg.Attachments {
		for _, block := range attachment.Blocks {
			if block.Text != nil {
				out += "\n" + block.Text.Text
			}
		}
	}

	for _, block := range msg.Blocks {
		if block.Text != nil {
			out += "\n" + block.Text.Text
		}
	}

	return &slackMessageForTest{all: out}
}

// TestSlackMentionsRenderedOnAlertEvents is the positive control: created and
// escalated messages lead with the ping.
func TestSlackMentionsRenderedOnAlertEvents(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	sender := &SlackSender{}
	targets := []MentionTarget{{UserUID: "u1", DisplayName: "Adam", ExternalID: "U-ADAM"}}

	for _, eventType := range []string{"incident.created", "incident.escalated"} {
		msg := sender.buildMessage(mentionPayload(eventType, targets))
		r.NotEmpty(msg.Attachments)

		first := msg.Attachments[0].Blocks[0]
		r.NotNil(first.Text, "the mention block must be first so it leads the message")
		r.Contains(first.Text.Text, "<@U-ADAM>")
	}
}

// TestSlackNoMentionsWhenNoneResolved pins the negative: with no targets the
// message carries no mention block and no `<@` at all.
func TestSlackNoMentionsWhenNoneResolved(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	sender := &SlackSender{}

	for _, eventType := range []string{"incident.created", "incident.escalated"} {
		flat := flatten(t, sender, mentionPayload(eventType, nil))
		r.NotContains(messageText(flat), "<@")
		r.NotContains(messageText(flat), "you are on call")
	}
}

// TestSlackResolvedAndReopenedNeverMention: even if the resolver somehow
// handed mentions to a status-update event, the rendered message carries none.
// The sender is the last line of defense behind the job-level gate.
func TestSlackResolvedAndReopenedNeverMention(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	sender := &SlackSender{}
	targets := []MentionTarget{{UserUID: "u1", DisplayName: "Adam", ExternalID: "U-ADAM"}}

	for _, eventType := range []string{"incident.resolved", "incident.reopened"} {
		payload := mentionPayload(eventType, targets)
		payload.Incident.ResolvedAt = &payload.Incident.StartedAt

		flat := flatten(t, sender, payload)
		r.NotContains(messageText(flat), "<@U-ADAM>",
			"%s must stay mention-free", eventType)
	}
}

// TestSlackMentionWithoutIdentityIsPlainText: a target with no external id is
// named but never pinged.
func TestSlackMentionWithoutIdentityIsPlainText(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	text := renderMentions([]MentionTarget{
		{UserUID: "u1", DisplayName: "Adam Unmapped"},
		{UserUID: "u2", DisplayName: "Zoe", ExternalID: "U-ZOE"},
	})

	r.Contains(text, "Adam Unmapped")
	r.Contains(text, "<@U-ZOE>")
	r.NotContains(text, "<@u1>")
}

// TestRenderMentionsEmptyCases: nothing to render, nothing rendered.
func TestRenderMentionsEmptyCases(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Empty(renderMentions(nil))
	r.Empty(renderMentions([]MentionTarget{}))
	// A target with neither an id nor a name contributes nothing rather than a
	// dangling separator.
	r.Empty(renderMentions([]MentionTarget{{UserUID: "u1"}}))
	r.Nil(mentionBlock(nil))
}
