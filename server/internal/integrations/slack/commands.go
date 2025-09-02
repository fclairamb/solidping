package slack

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// handleCommand routes commands to their specific handlers.
func (h *Handler) handleCommand(ctx context.Context, cmd *Command) (*MessageResponse, error) {
	slog.InfoContext(ctx, "Handling Slack command",
		"command", cmd.Command,
		"text", cmd.Text,
		"user_id", cmd.UserID,
		"team_id", cmd.TeamID,
		"channel_id", cmd.ChannelID,
	)

	switch cmd.Command {
	case "/check":
		return h.handleCheckCommand(ctx, cmd)
	default:
		return &MessageResponse{
			ResponseType: "ephemeral",
			Text:         "Unknown command: " + cmd.Command,
		}, nil
	}
}

// handleCheckCommand handles the /check command.
//
//nolint:funlen // This handler has clear sequential steps, splitting would reduce readability.
func (h *Handler) handleCheckCommand(ctx context.Context, cmd *Command) (*MessageResponse, error) {
	text := strings.TrimSpace(cmd.Text)

	// Show help if no URL provided
	if text == "" || text == "help" {
		return &MessageResponse{
			ResponseType: "ephemeral",
			Text:         "*Usage:* `/check <url>`\n\nExample: `/check https://example.com`",
		}, nil
	}

	// Validate URL
	parsedURL, parseErr := url.Parse(text)
	if parseErr != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		// Try adding https://
		if !strings.HasPrefix(text, "http://") && !strings.HasPrefix(text, "https://") {
			text = "https://" + text
			parsedURL, parseErr = url.Parse(text)
		}
	}

	if parseErr != nil || parsedURL.Host == "" {
		//nolint:nilerr // Intentionally returning user-friendly message without error
		return &MessageResponse{
			ResponseType: "ephemeral",
			Text:         "Invalid URL. Please provide a valid HTTP or HTTPS URL.\n\nExample: `/check https://example.com`",
		}, nil
	}

	// Get the connection to find the organization
	conn, err := h.svc.GetConnectionByTeamID(ctx, cmd.TeamID)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to get connection for command",
			"team_id", cmd.TeamID,
			"error", err,
		)

		return &MessageResponse{
			ResponseType: "ephemeral",
			Text:         "This Slack workspace is not connected to SolidPing. Please reconnect the app.",
		}, nil
	}

	// Get the Slack settings for the access token
	settings, err := models.SlackSettingsFromJSONMap(conn.Settings)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to parse Slack settings",
			"error", err,
		)

		return &MessageResponse{
			ResponseType: "ephemeral",
			Text:         "Configuration error. Please reconnect the Slack app.",
		}, nil
	}

	// Create the check
	checkResult, err := h.svc.CreateCheck(ctx, cmd.TeamID, text)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to create check",
			"url", text,
			"error", err,
		)

		return &MessageResponse{
			ResponseType: "ephemeral",
			Text:         "Failed to create check: " + err.Error(),
		}, nil
	}

	client := NewClient(settings.AccessToken)

	// Post the response message
	msg := &MessageResponse{
		Text: fmt.Sprintf("Check created: %s for %s", checkResult.Slug, parsedURL.Host), // Fallback text
		Blocks: []Block{
			{
				Type: "section",
				Text: &Text{
					Type: "mrkdwn",
					Text: fmt.Sprintf(":white_check_mark: *Check created:* `%s` for <%s|%s>", checkResult.Slug, text, parsedURL.Host),
				},
			},
			{
				Type: "context",
				Elements: []any{
					ContextElement{
						Type: "mrkdwn",
						Text: fmt.Sprintf("Created by <@%s>", cmd.UserID),
					},
				},
			},
		},
	}

	if _, err := client.PostMessage(ctx, PostMessageOptions{
		Channel:  cmd.ChannelID,
		ThreadTS: cmd.ThreadTS, // Reply in thread if command was in a thread
		Message:  msg,
	}); err != nil {
		slog.ErrorContext(ctx, "Failed to post message to Slack",
			"channel_id", cmd.ChannelID,
			"error", err,
		)

		return &MessageResponse{
			ResponseType: "ephemeral",
			Text:         "Failed to post message. Please try again.",
		}, nil
	}

	// Return nil since we posted via API (handler will return empty 200)
	//nolint:nilnil // Intentionally returning nil to signal empty 200 response.
	return nil, nil
}
