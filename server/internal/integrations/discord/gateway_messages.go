package discord

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/support"
)

// GatewayMessage is the subset of a MESSAGE_CREATE payload we act on.
//
//nolint:tagliatelle // Discord API uses snake_case
type GatewayMessage struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	GuildID   string `json:"guild_id,omitempty"`
	Content   string `json:"content"`
	Author    *User  `json:"author,omitempty"`
	// WebhookID is set on messages posted through a webhook — including a
	// SolidPing legacy-webhook alert, which must never be ingested as a comment
	// on itself.
	WebhookID string `json:"webhook_id,omitempty"`
	// Type distinguishes a real user message from a system notice (a pin, a
	// "started a thread" marker, a join message).
	Type int `json:"type"`
}

// Message types that carry human-authored content. Discord uses type 0 for a
// normal message and 19 for a reply; every other value is a system notice we
// must not treat as a comment.
const (
	messageTypeDefault = 0
	messageTypeReply   = 19
)

// IsHumanMessage reports whether this message is something a person typed.
func (m *GatewayMessage) IsHumanMessage() bool {
	if m.Author == nil || m.Author.Bot || m.WebhookID != "" {
		return false
	}

	return m.Type == messageTypeDefault || m.Type == messageTypeReply
}

// handleMessageCreate routes an inbound guild message.
//
// Two things can happen, and nothing else: the message mentions the bot and is
// a command, or the message is a reply inside an incident thread and may become
// an incident comment. Everything else — ordinary guild chatter, our own posts,
// webhook posts, system notices — is silently ignored.
func (g *GatewaySupervisor) handleMessageCreate(ctx context.Context, data json.RawMessage) {
	var msg GatewayMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		g.log.WarnContext(ctx, "Discord Gateway: undecodable MESSAGE_CREATE", "error", err)

		return
	}

	// IsHumanMessage already drops our own posts (Author.Bot) and webhook
	// posts, which is what stops an outbound reply from coming back in as a new
	// inbound message and making the thread talk to itself.
	if !msg.IsHumanMessage() {
		return
	}

	// A DM has no guild. It is a person talking to us, so it goes to the
	// support inbox rather than through the guild command/comment paths, both
	// of which key off GuildID and would do nothing useful with it.
	if msg.GuildID == "" {
		g.captureDirectMessage(ctx, &msg)

		return
	}

	if MentionsBot(msg.Content, g.BotUserID()) {
		g.handleMentionCommand(ctx, &msg)

		return
	}

	g.ingestThreadComment(ctx, &msg)
}

// handleMentionCommand runs an `@SolidPing …` mention through the same
// DispatchCommand the slash-command transport uses, and posts the reply where
// the mention was.
func (g *GatewaySupervisor) handleMentionCommand(ctx context.Context, msg *GatewayMessage) {
	cmd := ParseMentionText(msg.Content)
	cmd.GuildID = msg.GuildID
	cmd.ChannelID = msg.ChannelID
	cmd.UserID = msg.Author.ID
	cmd.UserName = msg.Author.DisplayName()

	// A mention typed inside a tracked incident thread carries that context, so
	// `@SolidPing comment …` works there without naming the incident.
	if _, _, ok := LookupThreadIncident(ctx, g.svc.db, msg.GuildID, msg.ChannelID); ok {
		cmd.ThreadID = msg.ChannelID
	}

	response, err := DispatchCommand(ctx, g.svc, cmd)
	if err != nil {
		g.log.WarnContext(ctx, "Discord Gateway: command dispatch failed",
			"guild_id", msg.GuildID, "command", cmd.Command, "error", err)

		return
	}

	if response == nil || response.Text == "" {
		return
	}

	client, err := g.svc.GetClient(ctx, msg.GuildID)
	if err != nil {
		return
	}

	// The Gateway has no ephemeral messages: an ephemeral-intended reply is
	// posted normally rather than dropped, because losing the answer is worse
	// than showing it to the channel that already saw the question.
	if _, err := client.CreateMessage(ctx, msg.ChannelID, &Message{
		Content:         response.Text,
		Components:      []Component{},
		AllowedMentions: &AllowedMentions{Parse: []string{}},
	}); err != nil {
		g.log.WarnContext(ctx, "Discord Gateway: failed to post command reply",
			"guild_id", msg.GuildID, "channel_id", msg.ChannelID, "error", err)
	}
}

// ingestThreadComment turns a human reply inside a tracked incident thread into
// an incident comment, when the integration opted into it.
func (g *GatewaySupervisor) ingestThreadComment(ctx context.Context, msg *GatewayMessage) {
	incidentUID, orgUID, ok := LookupThreadIncident(ctx, g.svc.db, msg.GuildID, msg.ChannelID)
	if !ok {
		return // a reply in a thread we don't track
	}

	if msg.Content == "" {
		// Empty content on a message that IS a human reply means the privileged
		// MESSAGE_CONTENT intent is not granted. Say so once per message rather
		// than silently doing nothing, because "the bot ignores everything" is
		// otherwise indistinguishable from a bug in this code.
		g.log.WarnContext(ctx, "Discord message has no content — is the MESSAGE_CONTENT intent enabled?",
			"guild_id", msg.GuildID, "channel_id", msg.ChannelID)

		return
	}

	if !g.ingestsAllThreadReplies(ctx, msg.GuildID) {
		g.log.DebugContext(ctx, "Skipping Discord thread reply: comment ingestion is explicit",
			"guild_id", msg.GuildID, "incident_uid", incidentUID)

		return
	}

	g.recordComment(ctx, msg, incidentUID, orgUID)
}

// recordComment dedupes on the message id, then appends the comment.
//
// Ordering is check → create → mark: the dedupe marker is written only after a
// successful comment, so a failed write leaves no marker and a Gateway
// redelivery re-attempts rather than being swallowed as a duplicate.
func (g *GatewaySupervisor) recordComment(
	ctx context.Context, msg *GatewayMessage, incidentUID, orgUID string,
) {
	dedupeKey := CommentDedupeStateKey(msg.GuildID, msg.ChannelID, msg.ID)

	existing, err := g.svc.db.GetStateEntry(ctx, &orgUID, dedupeKey)
	if err != nil {
		g.log.WarnContext(ctx, "Failed to check Discord comment dedupe", "error", err)

		return
	}

	if existing != nil {
		return // already ingested on an earlier delivery
	}

	if _, err := g.svc.incidentsService.AddCommentFromDiscord(
		ctx, orgUID, incidentUID, msg.Content,
		msg.Author.ID, msg.Author.DisplayName(), msg.GuildID, msg.ID,
	); err != nil {
		g.log.WarnContext(ctx, "Failed to add Discord thread reply as incident comment",
			"incident_uid", incidentUID, "error", err)

		return
	}

	if _, err := g.svc.db.SetStateEntryIfNotExists(
		ctx, &orgUID, dedupeKey, &models.JSONMap{ThreadIncidentUIDKey: incidentUID}, nil,
	); err != nil {
		g.log.WarnContext(ctx, "Failed to record Discord comment dedupe marker",
			"incident_uid", incidentUID, "error", err)
	}

	g.log.InfoContext(ctx, "Ingested Discord thread reply as incident comment",
		"incident_uid", incidentUID, "guild_id", msg.GuildID)
}

// ingestsAllThreadReplies reports whether the guild's integration is in "all"
// mode.
//
// Fails CLOSED: a guild whose connection or settings cannot be read is treated
// as explicit. Getting this wrong the other way writes somebody's private
// triage chatter into a permanent, fanned-out incident timeline.
func (g *GatewaySupervisor) ingestsAllThreadReplies(ctx context.Context, guildID string) bool {
	conn, err := g.svc.GetConnectionByGuildID(ctx, guildID)
	if err != nil || conn == nil {
		g.log.WarnContext(ctx, "Discord comment ingestion mode unreadable; defaulting to explicit",
			"guild_id", guildID, "error", err)

		return false
	}

	settings, err := models.DiscordSettingsFromJSONMap(conn.Settings)
	if err != nil {
		g.log.WarnContext(ctx, "Discord settings unparseable; defaulting to explicit comment ingestion",
			"guild_id", guildID, "error", err)

		return false
	}

	return settings.IngestsAllThreadReplies()
}

// captureDirectMessage records a Discord DM in the support inbox.
//
// Discord only delivers a DM if the user could open one with the bot in the
// first place (a shared guild, or a prior interaction), so this is a
// best-effort contact path rather than a guaranteed one.
func (g *GatewaySupervisor) captureDirectMessage(ctx context.Context, msg *GatewayMessage) {
	if g.svc == nil || g.svc.support == nil {
		g.log.DebugContext(ctx, "Discord DM ignored: support inbox not configured")

		return
	}

	if msg.Author == nil || msg.Author.ID == "" {
		return
	}

	// Redundant with IsHumanMessage's Author.Bot check, and kept anyway: this
	// is the one comparison that cannot be fooled by a payload that omits the
	// bot flag.
	if botID := g.BotUserID(); botID != "" && msg.Author.ID == botID {
		return
	}

	body := msg.Content
	if strings.TrimSpace(body) == "" {
		// An empty body here means the MESSAGE_CONTENT intent is not actually
		// granted. Record the fact rather than a blank line, so the cause is
		// legible in the inbox instead of looking like an empty message.
		body = "(empty Discord message — the bot may be missing the MESSAGE_CONTENT intent)"
	}

	g.svc.support.CaptureSafe(ctx, &support.Inbound{
		Channel:     models.SupportChannelDiscord,
		Identity:    msg.Author.ID,
		ExternalID:  msg.ID,
		Body:        body,
		SenderLabel: msg.Author.DisplayName(),
		Context:     map[string]any{"channelId": msg.ChannelID},
	})
}
