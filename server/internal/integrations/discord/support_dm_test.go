package discord

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/support"
)

type dmHarness struct {
	sup   *GatewaySupervisor
	inbox *support.Service
}

func newDMHarness(t *testing.T) *dmHarness {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	cfg := &config.Config{
		Discord: config.DiscordOAuthConfig{Enabled: true, GatewayEnabled: true, BotToken: "bot-token"},
	}

	svc := NewService(dbSvc, cfg, nil, nil)
	inbox := support.NewService(dbSvc, support.Options{BaseURL: "https://solidping.example"})
	svc.SetSupport(inbox)

	sup := NewGatewaySupervisor(svc, cfg, nil)
	sup.botUserID = "BOT-1"

	return &dmHarness{sup: sup, inbox: inbox}
}

func (h *dmHarness) deliver(t *testing.T, msg GatewayMessage) {
	t.Helper()

	raw, err := json.Marshal(msg)
	require.NoError(t, err)

	h.sup.handleMessageCreate(context.Background(), raw)
}

func (h *dmHarness) threads(t *testing.T) []*models.SupportThread {
	t.Helper()

	threads, err := h.inbox.ListThreads(context.Background(), models.ListSupportThreadsFilter{})
	require.NoError(t, err)

	return threads
}

// TestGatewayIntentsIncludeDirectMessages guards the constant that makes DMs
// arrive at all. Unlike Slack's im:history this needs no re-authorization — but
// without it the whole feature is dead on this channel.
func TestGatewayIntentsIncludeDirectMessages(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.NotZero(gatewayIntents&intentDirectMessages,
		"DIRECT_MESSAGES must be requested or Discord never delivers a DM")
	// MESSAGE_CONTENT must stay: without it DM bodies arrive empty, the same
	// silent failure the guild path already documents.
	r.NotZero(gatewayIntents & intentMessageContent)
	r.NotZero(gatewayIntents & intentGuildMessages)
	r.NotZero(gatewayIntents & intentGuilds)
}

// TestDirectMessageIsCaptured — a DM has no guild, and used to be dropped by
// the `msg.GuildID == ""` early return.
func TestDirectMessageIsCaptured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newDMHarness(t)

	h.deliver(t, GatewayMessage{
		ID: "M-1", ChannelID: "DM-1", Content: "hey, is the status page stale?",
		Author: &User{ID: "U-ALICE", Username: "alice"},
	})

	threads := h.threads(t)
	r.Len(threads, 1)
	r.Equal(models.SupportChannelDiscord, threads[0].Channel)
	r.Equal("U-ALICE", threads[0].ChannelIdentity)
	r.Equal("DM-1", threads[0].ChannelContext["channelId"], "reply routing must survive")

	messages, err := h.inbox.ListMessages(context.Background(), threads[0].UID, 0)
	r.NoError(err)
	r.Len(messages, 1)
	r.Equal("hey, is the status page stale?", messages[0].Body)
}

// TestGuildMessagesAreNotCaptured is the positive control for the split: guild
// traffic keeps its previous behavior and never becomes a support thread.
func TestGuildMessagesAreNotCaptured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newDMHarness(t)

	h.deliver(t, GatewayMessage{
		ID: "M-2", ChannelID: "C-GENERAL", GuildID: "G-1", Content: "morning all",
		Author: &User{ID: "U-ALICE", Username: "alice"},
	})
	r.Empty(h.threads(t), "a guild message must not open a support thread")

	// Same handler, no guild: captured. Without this the assertion above would
	// pass on a handler that captures nothing at all.
	h.deliver(t, GatewayMessage{
		ID: "M-3", ChannelID: "DM-1", Content: "psst",
		Author: &User{ID: "U-ALICE", Username: "alice"},
	})
	r.Len(h.threads(t), 1)
}

// TestOwnDirectMessagesAreNotRecaptured is the self-talking-thread guard: an
// outbound reply arrives back as a MESSAGE_CREATE and must be dropped.
func TestOwnDirectMessagesAreNotRecaptured(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newDMHarness(t)

	// Our own reply: Discord flags the author as a bot.
	h.deliver(t, GatewayMessage{
		ID: "M-4", ChannelID: "DM-1", Content: "we are on it",
		Author: &User{ID: "BOT-1", Username: "solidping", Bot: true},
	})
	r.Empty(h.threads(t))

	// Belt and braces: even a payload that forgot the bot flag is caught by the
	// bot-user-id comparison.
	h.deliver(t, GatewayMessage{
		ID: "M-5", ChannelID: "DM-1", Content: "still on it",
		Author: &User{ID: "BOT-1", Username: "solidping"},
	})
	r.Empty(h.threads(t))

	// A webhook post is not a human either.
	h.deliver(t, GatewayMessage{
		ID: "M-6", ChannelID: "DM-1", Content: "automated",
		Author: &User{ID: "U-HOOK"}, WebhookID: "W-1",
	})
	r.Empty(h.threads(t))

	// Positive control.
	h.deliver(t, GatewayMessage{
		ID: "M-7", ChannelID: "DM-1", Content: "thanks",
		Author: &User{ID: "U-ALICE", Username: "alice"},
	})
	r.Len(h.threads(t), 1)
}

// TestEmptyDMBodyRecordsTheLikelyCause — an empty content means the privileged
// MESSAGE_CONTENT intent is not actually granted. Recording the cause beats a
// blank line that reads like an empty message.
func TestEmptyDMBodyRecordsTheLikelyCause(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newDMHarness(t)

	h.deliver(t, GatewayMessage{
		ID: "M-8", ChannelID: "DM-1", Content: "",
		Author: &User{ID: "U-ALICE", Username: "alice"},
	})

	threads := h.threads(t)
	r.Len(threads, 1)

	messages, err := h.inbox.ListMessages(context.Background(), threads[0].UID, 0)
	r.NoError(err)
	r.Len(messages, 1)
	r.Contains(messages[0].Body, "MESSAGE_CONTENT")
}
