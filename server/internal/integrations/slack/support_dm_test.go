package slack

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/support"
)

const dmChannel = "D-IM-1"

// attachSupport wires a support inbox over the service's own database.
func attachSupport(t *testing.T, svc *Service) *support.Service {
	t.Helper()

	inbox := support.NewService(svc.db, support.Options{BaseURL: "https://solidping.example"})
	svc.support = inbox

	return inbox
}

func supportThreads(t *testing.T, inbox *support.Service) []*models.SupportThread {
	t.Helper()

	threads, err := inbox.ListThreads(context.Background(), models.ListSupportThreadsFilter{})
	require.NoError(t, err)

	return threads
}

// directMessage builds a `message` event with channel_type "im".
func directMessage(ts, user, text string) *Event {
	return &Event{
		TeamID: msgTeamID,
		Event: EventPayload{
			Type:        "message",
			Channel:     dmChannel,
			ChannelType: "im",
			Ts:          ts,
			User:        user,
			Text:        text,
		},
	}
}

// TestDirectMessageIsCapturedAndChannelTrafficIsNot pins the split the spec
// asks for: only channel_type == "im" is captured; channel messages and
// app_mentions keep their previous behavior exactly.
func TestDirectMessageIsCapturedAndChannelTrafficIsNot(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)
	inbox := attachSupport(t, svc)

	orgUID, incidentUID := seedIncidentThread(t, svc)
	fake := &fakeIncidentService{}
	svc.incidentsService = fake

	// A DM is captured.
	r.NoError(DispatchEvent(ctx, svc, directMessage("1700000000.000900", "U-ALICE", "hi, quick question")))

	threads := supportThreads(t, inbox)
	r.Len(threads, 1)
	r.Equal(models.SupportChannelSlack, threads[0].Channel)
	r.Equal("U-ALICE", threads[0].ChannelIdentity)
	// Reply routing has to survive, or an operator cannot answer.
	r.Equal(dmChannel, threads[0].ChannelContext["channelId"])
	r.Equal(msgTeamID, threads[0].ChannelContext["teamId"])

	messages, err := inbox.ListMessages(ctx, threads[0].UID, 0)
	r.NoError(err)
	r.Len(messages, 1)
	r.Equal("hi, quick question", messages[0].Body)

	// A channel THREAD REPLY still becomes an incident comment and is NOT
	// captured — the pre-existing behavior, unchanged.
	r.NoError(DispatchEvent(ctx, svc, threadReply("1700000000.000901", "U-ALICE", "restarting the pod")))
	r.Len(fake.comments, 1)
	r.Equal(orgUID, fake.comments[0].orgUID)
	r.Equal(incidentUID, fake.comments[0].incidentUID)
	r.Len(supportThreads(t, inbox), 1, "a channel thread reply must not become a support thread")

	// A top-level CHANNEL message is still ignored entirely.
	topLevel := &Event{
		TeamID: msgTeamID,
		Event: EventPayload{
			Type: "message", Channel: msgChannel, ChannelType: "channel",
			Ts: "1700000000.000902", User: "U-ALICE", Text: "morning all",
		},
	}
	r.NoError(DispatchEvent(ctx, svc, topLevel))
	r.Len(supportThreads(t, inbox), 1)
	r.Len(fake.comments, 1)

	// An app_mention is a different event type and never reaches handleMessage.
	mention := &Event{
		TeamID: msgTeamID,
		Event: EventPayload{
			Type: "app_mention", Channel: msgChannel,
			Ts: "1700000000.000903", User: "U-ALICE", Text: "<@U-BOT> help",
		},
	}
	// It fails to POST its answer here (no fake Slack API is wired), which is
	// exactly the pre-existing behavior — what matters is that it does not
	// route through the support inbox.
	_ = DispatchEvent(ctx, svc, mention)
	r.Len(supportThreads(t, inbox), 1, "an app_mention must not become a support thread")
}

// TestOutboundReplyIsNotRecaptured is the self-talking-thread guard. It only
// shows up once replying works: our own DM reply comes back as a message.im
// event, and if it were captured the thread would answer itself forever.
func TestOutboundReplyIsNotRecaptured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc := setupSlackService(t)
	inbox := attachSupport(t, svc)
	seedIncidentThread(t, svc)

	// Our own post carries a bot_id.
	ourReply := directMessage("1700000000.001000", "U-BOT", "we are looking into it")
	ourReply.Event.BotID = "B-SOLIDPING"

	r.NoError(DispatchEvent(ctx, svc, ourReply))
	r.Empty(supportThreads(t, inbox), "a bot-authored DM must not be captured")

	// A message_changed subtype is likewise not a new human message.
	edited := directMessage("1700000000.001001", "U-ALICE", "typo fixed")
	edited.Event.Subtype = "message_changed"

	r.NoError(DispatchEvent(ctx, svc, edited))
	r.Empty(supportThreads(t, inbox))

	// Positive control: a plain human DM on the same handler IS captured, so
	// the two assertions above are not passing because capture never runs.
	r.NoError(DispatchEvent(ctx, svc, directMessage("1700000000.001002", "U-ALICE", "any news?")))
	r.Len(supportThreads(t, inbox), 1)
}

// TestDMCaptureAvailableReflectsTheGrantedScopes is the "degrade cleanly and
// observably" half of shipping Slack DM support: Slack does not grant new
// scopes to an existing install, so a workspace connected before im:history was
// requested silently never delivers message.im. The state has to be readable
// rather than looking like an empty inbox.
func TestDMCaptureAvailableReflectsTheGrantedScopes(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	stale := &models.SlackSettings{Scopes: []string{"chat:write", "channels:read"}}
	r.False(stale.DMCaptureAvailable(), "a pre-im:history install must report DM capture unavailable")

	fresh := &models.SlackSettings{Scopes: []string{"chat:write", models.SlackScopeIMHistory}}
	r.True(fresh.DMCaptureAvailable())

	// A settings blob with no scopes at all (an install old enough not to have
	// recorded them) also fails closed.
	r.False((&models.SlackSettings{}).DMCaptureAvailable())
	r.False((*models.SlackSettings)(nil).DMCaptureAvailable())
}

// TestInstallRequestsIMHistory guards the actual blocker: the manifests already
// declared im:history and already subscribed message.im, but the code's install
// request did not ask for it, so Slack never granted it.
func TestInstallRequestsIMHistory(t *testing.T) {
	t.Parallel()

	require.Contains(t, slackBotScopes, models.SlackScopeIMHistory,
		"the OAuth install must request im:history or Slack never delivers a DM")
}
