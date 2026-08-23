package slack

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/support"
)

// handleEvent routes events to their specific handlers (HTTP transport entry point).
func (h *Handler) handleEvent(ctx context.Context, event *Event) error {
	return DispatchEvent(ctx, h.svc, event)
}

// DispatchEvent is the transport-agnostic dispatch entry for Slack events.
// Both the HTTP webhook handler and the Socket Mode supervisor route through it.
func DispatchEvent(ctx context.Context, svc *Service, event *Event) error {
	slog.InfoContext(ctx, "Handling Slack event",
		"event_type", event.Event.Type,
		"team_id", event.TeamID,
	)

	// Construct a transport-less *Handler bound only to svc — every inner
	// dispatch helper here uses dispatcher.svc; cfg is only needed for HTTP
	// install / signature paths and is not reachable from the dispatch chain.
	dispatcher := &Handler{svc: svc}

	switch event.Event.Type {
	case "app_home_opened":
		return dispatcher.handleAppHomeOpened(ctx, event)
	case "app_mention":
		return dispatcher.handleAppMention(ctx, event)
	case "app_uninstalled":
		return dispatcher.handleAppUninstalled(ctx, event)
	case "link_shared":
		return dispatcher.handleLinkShared(ctx, event)
	case "member_joined_channel":
		return dispatcher.handleMemberJoinedChannel(ctx, event)
	case eventTypeMessage:
		return dispatcher.handleMessage(ctx, event)
	default:
		slog.DebugContext(ctx, "Unhandled event type", "type", event.Event.Type)

		return nil
	}
}

// handleAppHomeOpened handles the app_home_opened event.
func (h *Handler) handleAppHomeOpened(ctx context.Context, event *Event) error {
	// Only handle the "home" tab
	if event.Event.Tab != "home" {
		return nil
	}

	client, err := h.svc.GetClient(ctx, event.TeamID)
	if err != nil {
		return fmt.Errorf("failed to get client: %w", err)
	}

	view := h.buildAppHomeView(ctx, event.TeamID)

	if err := client.PublishView(ctx, event.Event.User, view); err != nil {
		return fmt.Errorf("failed to publish view: %w", err)
	}

	return nil
}

// handleAppMention handles the app_mention event.
func (h *Handler) handleAppMention(ctx context.Context, event *Event) error {
	// Parse the command from the mention text
	cmd := ParseMentionText(event.Event.Text)

	slog.InfoContext(ctx, "Parsed mention command",
		"command", cmd.Command,
		"subcommand", cmd.Subcommand,
		"args", cmd.Args,
		"flags", cmd.Flags,
	)

	// Route to command handler
	return h.handleMentionCommand(ctx, event, cmd)
}

// handleAppUninstalled handles the app_uninstalled event.
func (h *Handler) handleAppUninstalled(ctx context.Context, event *Event) error {
	return h.svc.HandleAppUninstalled(ctx, event.TeamID)
}

// handleLinkShared handles the link_shared event for link unfurling.
func (h *Handler) handleLinkShared(ctx context.Context, event *Event) error {
	client, err := h.svc.GetClient(ctx, event.TeamID)
	if err != nil {
		return fmt.Errorf("failed to get client: %w", err)
	}

	unfurls := make(map[string]Unfurl)

	// Only unfurl links pointing at THIS instance. Derived from server.base_url
	// rather than hardcoded, so a self-hosted install unfurls its own links and
	// no deployment's hostname is baked into the binary.
	ownHost := h.cfg.BaseURLHost()
	if ownHost == "" {
		return nil
	}

	for i := range event.Event.Links {
		link := &event.Event.Links[i]

		if !strings.EqualFold(link.Domain, ownHost) {
			continue
		}

		unfurl := h.buildLinkUnfurl(ctx, link.URL)
		if unfurl != nil {
			unfurls[link.URL] = *unfurl
		}
	}

	if len(unfurls) == 0 {
		return nil
	}

	if err := client.UnfurlLinks(ctx, event.Event.Channel, event.Event.MessageTs, unfurls); err != nil {
		return fmt.Errorf("failed to unfurl links: %w", err)
	}

	return nil
}

// buildAppHomeView builds the App Home view.
func (h *Handler) buildAppHomeView(_ context.Context, _ string) *AppHomeView {
	return &AppHomeView{
		Type: "home",
		Blocks: []Block{
			{
				Type: "header",
				Text: &Text{
					Type: BlockTypePlainText,
					Text: "Welcome to SolidPing",
				},
			},
			{
				Type: BlockTypeSection,
				Text: &Text{
					Type: BlockTypeMrkdwn,
					Text: "SolidPing monitors your endpoints and alerts you when things go wrong.",
				},
			},
			{
				Type: "divider",
			},
			{
				Type: BlockTypeSection,
				Text: &Text{
					Type: BlockTypeMrkdwn,
					Text: "*Quick Actions*",
				},
			},
			{
				Type: "actions",
				Elements: []any{
					Element{
						Type: "button",
						Text: &Text{
							Type: BlockTypePlainText,
							Text: "Add a Check",
						},
						ActionID: ActionAddCheck,
						Style:    "primary",
					},
					Element{
						Type: "button",
						Text: &Text{
							Type: BlockTypePlainText,
							Text: "View Dashboard",
						},
						ActionID: "view_dashboard",
						URL:      "https://solidping.io/dashboard",
					},
				},
			},
			{
				Type: "divider",
			},
			{
				Type: BlockTypeContext,
				Elements: []any{
					ContextElement{
						Type: BlockTypeMrkdwn,
						Text: "Use `/check <url>` to quickly create a new check",
					},
				},
			},
		},
	}
}

// buildLinkUnfurl builds an unfurl preview for a SolidPing link.
func (h *Handler) buildLinkUnfurl(_ context.Context, linkURL string) *Unfurl {
	// TODO: Parse the URL and fetch actual check/incident data
	// For now, return a simple preview

	return &Unfurl{
		Blocks: []Block{
			{
				Type: BlockTypeSection,
				Text: &Text{
					Type: BlockTypeMrkdwn,
					Text: fmt.Sprintf("*SolidPing Link*\n<%s|View in SolidPing>", linkURL),
				},
			},
		},
	}
}

// State-entry key schema shared between the notification sender (which writes
// the reverse thread mapping when it posts an incident's top-level message)
// and this reader (which resolves an inbound reply back to its incident).
const (
	reverseThreadStatePrefix = "slack/threads/"
	commentDedupeStatePrefix = "slack/comments/"

	// ThreadIncidentUIDKey / ThreadOrgUIDKey are the value keys of a reverse
	// thread entry. Exported so the writer and reader can't drift.
	ThreadIncidentUIDKey = "incident_uid"
	ThreadOrgUIDKey      = "organization_uid"
)

// ReverseThreadStateKey builds the state_entries key mapping a Slack thread
// (team, channel, thread_ts) back to its incident. Stored as a global
// (org-nil) entry: the workspace's home org need not own the incident, so the
// value carries the incident's org UID rather than scoping the key to it.
func ReverseThreadStateKey(teamID, channelID, threadTS string) string {
	return reverseThreadStatePrefix + teamID + "/" + channelID + "/" + threadTS
}

// commentDedupeStateKey builds the per-message idempotency marker key that
// stops Slack event redelivery (Events API retries, socket-mode reconnects)
// from posting the same reply twice.
func commentDedupeStateKey(teamID, channelID, ts string) string {
	return commentDedupeStatePrefix + teamID + "/" + channelID + "/" + ts
}

// handleMessage ingests a Slack channel message as an incident comment when it
// is a human thread reply under a known incident thread. Everything else —
// top-level messages, edits/deletes, the bot's own posts, replies in unrelated
// threads — is silently ignored. Reached from both the HTTP Events API and
// Socket Mode, since both funnel through DispatchEvent.
func (h *Handler) handleMessage(ctx context.Context, event *Event) error {
	msg := &event.Event

	// Ignore message subtypes (message_changed, message_deleted, bot_message,
	// channel_join, …) and any bot-authored post — including our own
	// resolve/reopen thread replies, which carry a bot_id.
	//
	// This gate is also what keeps a DM thread from talking to itself: our own
	// reply comes back as a message.im event carrying a bot_id, and is dropped
	// here before any capture happens.
	if msg.Subtype != "" || msg.BotID != "" {
		return nil
	}

	// A DIRECT MESSAGE is a person talking to us, not incident chatter. It is
	// captured in the support inbox and never treated as a comment (spec
	// 2026-08-22-02). Channel and group messages below are untouched.
	//
	// This branch sits ABOVE the thread-reply gate deliberately: a DM's first
	// message has no thread_ts, so the gate below would drop every DM.
	if msg.ChannelType == slackChannelTypeIM {
		h.captureDirectMessage(ctx, event)

		return nil
	}

	// Only thread replies become comments. A top-level message has no
	// thread_ts, or a thread_ts equal to its own ts (Slack sets thread_ts on
	// the parent once a thread exists).
	if msg.ThreadTs == "" || msg.ThreadTs == msg.Ts {
		return nil
	}

	incidentUID, orgUID, ok, err := h.resolveIncidentThread(ctx, event.TeamID, msg.Channel, msg.ThreadTs)
	if err != nil {
		return err
	}
	if !ok {
		return nil // reply in a thread we don't track — ignore
	}

	// Explicit-by-default (spec 2026-08-15-08): a plain thread reply is
	// conversation, not an incident comment. Only a workspace that has opted
	// into `comment_ingestion: "all"` keeps the historical capture-everything
	// behavior; everyone else uses `/comment`.
	if !h.ingestsAllThreadReplies(ctx, event.TeamID) {
		slog.DebugContext(ctx, "Skipping Slack thread reply: comment ingestion is explicit",
			"team_id", event.TeamID, "incident_uid", incidentUID)

		return nil
	}

	return h.ingestSlackComment(ctx, event, incidentUID, orgUID)
}

// ingestsAllThreadReplies reports whether the workspace's SolidPing
// integration is in "all" mode.
//
// Fails CLOSED: a workspace whose connection or settings cannot be read is
// treated as explicit. Getting this wrong in the other direction writes
// somebody's private triage chatter into a permanent, fanned-out incident
// timeline — an unreadable setting must never do that.
func (h *Handler) ingestsAllThreadReplies(ctx context.Context, teamID string) bool {
	conn, err := h.svc.GetConnectionByTeamID(ctx, teamID)
	if err != nil || conn == nil {
		slog.WarnContext(ctx, "Slack comment ingestion mode unreadable; defaulting to explicit",
			"team_id", teamID, "error", err)

		return false
	}

	settings, err := models.SlackSettingsFromJSONMap(conn.Settings)
	if err != nil {
		slog.WarnContext(ctx, "Slack settings unparseable; defaulting to explicit comment ingestion",
			"team_id", teamID, "error", err)

		return false
	}

	return settings.IngestsAllThreadReplies()
}

// resolveIncidentThread looks up the incident a Slack thread belongs to via the
// reverse mapping. The bool is false (with a nil error) when the thread is
// untracked. Returns (incidentUID, orgUID, found, error).
func (h *Handler) resolveIncidentThread(
	ctx context.Context, teamID, channelID, threadTS string,
) (string, string, bool, error) {
	entry, err := h.svc.db.GetStateEntry(ctx, nil, ReverseThreadStateKey(teamID, channelID, threadTS))
	if err != nil {
		return "", "", false, fmt.Errorf("looking up incident thread: %w", err)
	}
	if entry == nil || entry.Value == nil {
		return "", "", false, nil
	}

	incidentUID, _ := (*entry.Value)[ThreadIncidentUIDKey].(string)
	orgUID, _ := (*entry.Value)[ThreadOrgUIDKey].(string)
	if incidentUID == "" || orgUID == "" {
		return "", "", false, nil
	}

	return incidentUID, orgUID, true, nil
}

// ingestSlackComment dedupes on the message ts, resolves the author's display
// name best-effort, and appends the comment. Ordering is check → create →
// mark: the dedupe marker is written only after a successful comment, so a
// failed write leaves no marker and Slack's retry re-attempts rather than being
// swallowed as a duplicate. A tracked incident thread's replies are low volume,
// so the tiny check-then-write window (a genuinely concurrent double-delivery —
// which Slack does not do; retries are seconds apart) is an acceptable trade
// for never losing a comment.
func (h *Handler) ingestSlackComment(ctx context.Context, event *Event, incidentUID, orgUID string) error {
	msg := &event.Event
	dedupeKey := commentDedupeStateKey(event.TeamID, msg.Channel, msg.Ts)

	existing, err := h.svc.db.GetStateEntry(ctx, &orgUID, dedupeKey)
	if err != nil {
		return fmt.Errorf("checking slack comment dedupe: %w", err)
	}
	if existing != nil {
		return nil // already ingested on an earlier delivery
	}

	displayName := h.resolveSlackUserName(ctx, event.TeamID, msg.User)

	if _, err := h.svc.incidentsService.AddCommentFromSlack(
		ctx, orgUID, incidentUID, msg.Text, msg.User, displayName, event.TeamID, msg.Ts,
	); err != nil {
		// No marker written yet, so a redelivery retries rather than being
		// silently dropped.
		return fmt.Errorf("adding slack comment: %w", err)
	}

	// Record the marker so redelivery is a no-op. Best-effort: a marker write
	// failure at worst allows a duplicate on redelivery, never a lost comment.
	if _, err := h.svc.db.SetStateEntryIfNotExists(
		ctx, &orgUID, dedupeKey, &models.JSONMap{ThreadIncidentUIDKey: incidentUID}, nil,
	); err != nil {
		slog.WarnContext(ctx, "Failed to record Slack comment dedupe marker",
			"incident_uid", incidentUID, "error", err)
	}

	slog.InfoContext(ctx, "Ingested Slack thread reply as incident comment",
		"incident_uid", incidentUID,
		"team_id", event.TeamID,
		"channel_id", msg.Channel,
	)

	return nil
}

// resolveSlackUserName fetches the author's Slack display handle best-effort;
// on any failure it returns "" and the comment records only the user ID.
func (h *Handler) resolveSlackUserName(ctx context.Context, teamID, userID string) string {
	if userID == "" {
		return ""
	}

	client, err := h.svc.GetClient(ctx, teamID)
	if err != nil {
		return ""
	}

	user, err := client.GetUserInfo(ctx, userID)
	if err != nil || user == nil {
		return ""
	}

	return user.Name
}

// handleMemberJoinedChannel handles when the bot is invited to a channel.
// If no default channel is configured, auto-configure this channel as the default.
func (h *Handler) handleMemberJoinedChannel(ctx context.Context, event *Event) error {
	// Get the connection to check if we're the joining user
	conn, err := h.svc.GetConnectionByTeamID(ctx, event.TeamID)
	if err != nil {
		slog.WarnContext(ctx, "Failed to get connection for member_joined_channel",
			"team_id", event.TeamID,
			"error", err,
		)
		return nil // Don't fail the event
	}

	settings, err := models.SlackSettingsFromJSONMap(conn.Settings)
	if err != nil {
		slog.WarnContext(ctx, "Failed to parse settings for member_joined_channel",
			"team_id", event.TeamID,
			"error", err,
		)
		return nil
	}

	// Check if the joining user is our bot
	if event.Event.User != settings.BotUserID {
		return nil // Not our bot, ignore
	}

	slog.InfoContext(ctx, "Bot joined channel",
		"channel_id", event.Event.Channel,
		"team_id", event.TeamID,
	)

	// Check if default channel is already set
	if settings.ChannelID != "" {
		slog.DebugContext(ctx, "Default channel already set, skipping auto-configure",
			"existing_channel", settings.ChannelID,
		)
		return nil
	}

	// Auto-configure this channel as default and send welcome message
	return h.svc.SetDefaultChannel(ctx, event.TeamID, event.Event.Channel, true)
}

// slackChannelTypeIM is the channel_type Slack stamps on a direct message.
const slackChannelTypeIM = "im"

// eventTypeMessage is the Slack `message` event type.
const eventTypeMessage = "message"

// captureDirectMessage records a Slack DM in the support inbox.
//
// Best-effort and non-fatal by construction: a capture problem must not turn
// into a non-2xx on the Events API, which Slack answers by retrying and then by
// disabling the subscription. CaptureSafe never returns an error.
func (h *Handler) captureDirectMessage(ctx context.Context, event *Event) {
	msg := &event.Event

	if h.svc == nil || h.svc.support == nil {
		slog.DebugContext(ctx, "Slack DM ignored: support inbox not configured",
			"team_id", event.TeamID)

		return
	}

	if msg.User == "" {
		return
	}

	// Belt and braces on top of the BotID check in handleMessage: compare the
	// author against the workspace's own bot user id too. A message we posted
	// through a path that did not stamp a bot_id would otherwise be captured as
	// an inbound support message and answered by us, forever.
	if botUserID := h.botUserID(ctx, event.TeamID); botUserID != "" && msg.User == botUserID {
		return
	}

	body := msg.Text
	if strings.TrimSpace(body) == "" {
		body = "(empty Slack message)"
	}

	h.svc.support.CaptureSafe(ctx, &support.Inbound{
		Channel:  models.SupportChannelSlack,
		Identity: msg.User,
		// (team, channel, ts) is unique per message; ts alone is not.
		ExternalID: event.TeamID + ":" + msg.Channel + ":" + msg.Ts,
		Body:       body,
		Context: map[string]any{
			"teamId":    event.TeamID,
			"channelId": msg.Channel,
		},
	})
}

// botUserID reads the workspace's stored bot user id. Returns "" when the
// connection or settings cannot be read — the BotID check has already done the
// primary job, so an unreadable setting must not block capture.
func (h *Handler) botUserID(ctx context.Context, teamID string) string {
	conn, err := h.svc.GetConnectionByTeamID(ctx, teamID)
	if err != nil || conn == nil {
		return ""
	}

	settings, err := models.SlackSettingsFromJSONMap(conn.Settings)
	if err != nil || settings == nil {
		return ""
	}

	return settings.BotUserID
}
