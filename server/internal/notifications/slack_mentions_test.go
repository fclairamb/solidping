package notifications

import (
	"encoding/json"
	"strings"
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

	var out strings.Builder

	out.WriteString(msg.Text)

	for i := range msg.Attachments {
		for j := range msg.Attachments[i].Blocks {
			if block := msg.Attachments[i].Blocks[j]; block.Text != nil {
				out.WriteString("\n" + block.Text.Text)
			}
		}
	}

	for i := range msg.Blocks {
		if msg.Blocks[i].Text != nil {
			out.WriteString("\n" + msg.Blocks[i].Text.Text)
		}
	}

	return &slackMessageForTest{all: out.String()}
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
		r.NotContains(messageText(flat), "On call:")
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

// TestRenderMentionsOnCallWording pins §3's wording across 0, 1 and 2 targets
// and with a handle-less target. The line is a statement to the channel about
// who is on call, never a sentence addressed to them.
func TestRenderMentionsOnCallWording(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		targets []MentionTarget
		want    string
	}{
		{
			name:    "no targets says nothing at all",
			targets: nil,
			want:    "",
		},
		{
			name:    "one mapped target",
			targets: []MentionTarget{{UserUID: "u1", DisplayName: "Adam", ExternalID: "U-ADAM"}},
			want:    "On call: <@U-ADAM>",
		},
		{
			name: "two mapped targets are comma separated",
			targets: []MentionTarget{
				{UserUID: "u1", DisplayName: "Adam", ExternalID: "U-ADAM"},
				{UserUID: "u2", DisplayName: "Zoe", ExternalID: "U-ZOE"},
			},
			want: "On call: <@U-ADAM>, <@U-ZOE>",
		},
		{
			name:    "a target with no handle is named, not addressed",
			targets: []MentionTarget{{UserUID: "u1", DisplayName: "Alice"}},
			want:    "On call: Alice",
		},
		{
			name: "mixed: one pinged, one merely named",
			targets: []MentionTarget{
				{UserUID: "u1", DisplayName: "Alice"},
				{UserUID: "u2", DisplayName: "Zoe", ExternalID: "U-ZOE"},
			},
			want: "On call: Alice, <@U-ZOE>",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tc.want, renderMentions(tc.targets))
		})
	}
}

// TestSlackMessageWithNothingToSayIsByteIdentical is the guarantee that makes
// this feature safe to ship to every existing install: when the resolver has
// nothing to say, the message bytes are EXACTLY the ones the sender produced
// before mentions existed.
//
// Asserted on the serialized message rather than on block counts, because a
// stray empty block, a changed fallback text or a reordered attachment would
// all pass a structural check and still change what Slack renders.
func TestSlackMessageWithNothingToSayIsByteIdentical(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	sender := &SlackSender{}

	for _, eventType := range []string{"incident.created", "incident.escalated"} {
		baseline, err := json.Marshal(sender.buildMessage(mentionPayload(eventType, nil)))
		r.NoError(err)

		// A resolved target that renders to nothing: no handle AND no name.
		// The sender must treat it exactly like "no mentions at all".
		empty, err := json.Marshal(sender.buildMessage(
			mentionPayload(eventType, []MentionTarget{{UserUID: "u1"}})))
		r.NoError(err)

		r.Equal(string(baseline), string(empty),
			"%s with nothing to say must be byte-identical to the mention-free message", eventType)

		// Positive control: a real target DOES change the bytes, so the
		// equality above is a property of emptiness and not of the comparison.
		withMention, err := json.Marshal(sender.buildMessage(
			mentionPayload(eventType, []MentionTarget{{UserUID: "u1", ExternalID: "U-ADAM"}})))
		r.NoError(err)
		r.NotEqual(string(baseline), string(withMention))
	}
}
