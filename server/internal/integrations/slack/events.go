package slack

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// handleEvent routes events to their specific handlers.
func (h *Handler) handleEvent(ctx context.Context, event *Event) error {
	slog.InfoContext(ctx, "Handling Slack event",
		"event_type", event.Event.Type,
		"team_id", event.TeamID,
	)

	switch event.Event.Type {
	case "app_home_opened":
		return h.handleAppHomeOpened(ctx, event)
	case "app_mention":
		return h.handleAppMention(ctx, event)
	case "app_uninstalled":
		return h.handleAppUninstalled(ctx, event)
	case "link_shared":
		return h.handleLinkShared(ctx, event)
	case "member_joined_channel":
		return h.handleMemberJoinedChannel(ctx, event)
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

	for i := range event.Event.Links {
		link := &event.Event.Links[i]
		// Only unfurl SolidPing links
		if link.Domain != "solidping.io" && link.Domain != "solidping.k8xp.com" {
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
					Type: "plain_text",
					Text: "Welcome to SolidPing",
				},
			},
			{
				Type: "section",
				Text: &Text{
					Type: "mrkdwn",
					Text: "SolidPing monitors your endpoints and alerts you when things go wrong.",
				},
			},
			{
				Type: "divider",
			},
			{
				Type: "section",
				Text: &Text{
					Type: "mrkdwn",
					Text: "*Quick Actions*",
				},
			},
			{
				Type: "actions",
				Elements: []any{
					Element{
						Type: "button",
						Text: &Text{
							Type: "plain_text",
							Text: "Add a Check",
						},
						ActionID: "add_check",
						Style:    "primary",
					},
					Element{
						Type: "button",
						Text: &Text{
							Type: "plain_text",
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
				Type: "context",
				Elements: []any{
					ContextElement{
						Type: "mrkdwn",
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
				Type: "section",
				Text: &Text{
					Type: "mrkdwn",
					Text: fmt.Sprintf("*SolidPing Link*\n<%s|View in SolidPing>", linkURL),
				},
			},
		},
	}
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
