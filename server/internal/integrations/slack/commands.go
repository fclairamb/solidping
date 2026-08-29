package slack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
)

// handleCommand routes commands to their specific handlers (HTTP transport entry point).
func (h *Handler) handleCommand(ctx context.Context, cmd *Command) (*MessageResponse, error) {
	return DispatchCommand(ctx, h.svc, cmd)
}

// DispatchCommand is the transport-agnostic dispatch entry for Slack slash commands.
// Returns nil response when the response is posted out-of-band via chat.postMessage,
// or a MessageResponse to be returned synchronously.
func DispatchCommand(ctx context.Context, svc *Service, cmd *Command) (*MessageResponse, error) {
	slog.InfoContext(ctx, "Handling Slack command",
		"command", cmd.Command,
		"text", cmd.Text,
		"user_id", cmd.UserID,
		"team_id", cmd.TeamID,
		"channel_id", cmd.ChannelID,
	)

	dispatcher := &Handler{svc: svc}

	switch cmd.Command {
	case "/solidping":
		return dispatcher.handleSolidpingCommand(ctx, cmd)
	case "/check":
		// Retired standalone command — kept registered (not deleted) because a
		// workspace that installed the old manifest keeps it registered with
		// Slack until it re-authorizes; see legacyMovedNotice. Synchronous and
		// side-effect-free: it never touches the DB or Slack's API, so it
		// cannot blow the 3-second ACK budget.
		return legacyMovedNotice("/check", "/solidping check <url>"), nil
	case "/comment":
		return legacyMovedNotice("/comment", "/solidping comment [#N] <text>"), nil
	default:
		return &MessageResponse{
			ResponseType: ResponseTypeEphemeral,
			Text: fmt.Sprintf(
				"Unknown command: %s. Type `/solidping help` for available commands.", cmd.Command,
			),
		}, nil
	}
}

// handleCheckCommand handles the `check` subcommand of `/solidping`
// (reached via handleSolidpingCommand in solidping_command.go).
//
// Validation is synchronous (fast, local, well inside Slack's 3-second ACK
// budget). Once the URL is accepted, the real work — a DB write, formerly
// followed by a synchronous outbound chat.postMessage call to Slack's API —
// is NOT: either can be slow under load or a network hiccup, and running
// them before the HTTP response is written risks Slack's own client timing
// out and showing the user an error even though the check was actually
// created. So the handler ACKs immediately (ephemeral) and finishes the real
// work in a detached goroutine, reporting the outcome — success or failure,
// never a silent drop — via cmd.ResponseURL (postResponseURL), Slack's
// documented mechanism for answering after the initial window.
func (h *Handler) handleCheckCommand(ctx context.Context, cmd *Command) (*MessageResponse, error) {
	text := strings.TrimSpace(cmd.Text)

	// Show help if no URL provided
	if text == "" || text == cmdHelp {
		return &MessageResponse{
			ResponseType: ResponseTypeEphemeral,
			Text:         "*Usage:* `/solidping check <url>`\n\nExample: `/solidping check https://example.com`",
		}, nil
	}

	normalizedURL, parsedURL, ok := normalizeCheckURL(text)
	if !ok {
		return &MessageResponse{
			ResponseType: ResponseTypeEphemeral,
			Text: "Invalid URL. Please provide a valid HTTP or HTTPS URL.\n\n" +
				"Example: `/solidping check https://example.com`",
		}, nil
	}

	if cmd.ResponseURL == "" {
		// Defensive fallback for a caller that doesn't set response_url (Slack
		// always does on a real slash command) — do the work synchronously
		// rather than silently dropping it. Loses the ACK-budget protection,
		// but that only matters when the fast path above is unavailable.
		slog.WarnContext(ctx, "/solidping check invoked with no response_url; falling back to synchronous handling",
			"team_id", cmd.TeamID)

		return h.createCheckAndReply(ctx, cmd, normalizedURL, parsedURL), nil
	}

	// Detach from the request context: it is normally canceled once this
	// handler returns and the HTTP response is written, which would abort
	// the goroutine's work before it gets to report anything. A fresh,
	// bounded timeout replaces it.
	detached := context.WithoutCancel(ctx)

	go h.finishCheckCommandAsync(detached, cmd, normalizedURL, parsedURL)

	return &MessageResponse{
		ResponseType: ResponseTypeEphemeral,
		Text:         fmt.Sprintf(":hourglass_flowing_sand: Creating check for `%s`…", normalizedURL),
	}, nil
}

// normalizeCheckURL validates text as an HTTP(S) URL, prefixing https:// onto
// a bare host if needed. The second return is the parsed URL; the third is
// false when text could not be turned into a valid http(s) URL.
func normalizeCheckURL(text string) (string, *url.URL, bool) {
	parsedURL, parseErr := url.Parse(text)
	if parseErr != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		if !strings.HasPrefix(text, "http://") && !strings.HasPrefix(text, "https://") {
			text = "https://" + text
			parsedURL, parseErr = url.Parse(text)
		}
	}

	if parseErr != nil || parsedURL == nil || parsedURL.Host == "" {
		return "", nil, false
	}

	return text, parsedURL, true
}

// finishCheckCommandAsync performs the check creation and reports the
// outcome to Slack's response_url. Runs after handleCheckCommand has already
// returned its ephemeral ACK.
func (h *Handler) finishCheckCommandAsync(
	parent context.Context, cmd *Command, normalizedURL string, parsedURL *url.URL,
) {
	ctx, cancel := context.WithTimeout(parent, asyncCheckTimeout)
	defer cancel()

	resp := h.createCheckAndReply(ctx, cmd, normalizedURL, parsedURL)

	if err := postResponseURL(ctx, cmd.ResponseURL, resp); err != nil {
		slog.ErrorContext(ctx, "Failed to post /solidping check follow-up to response_url",
			"team_id", cmd.TeamID, "channel_id", cmd.ChannelID, "error", err)
	}
}

// createCheckAndReply performs the actual check creation and builds the
// message reporting the outcome — success (posted in-channel, since a new
// check is worth the team seeing) or failure (ephemeral, so it never gets
// silently swallowed).
func (h *Handler) createCheckAndReply(
	ctx context.Context, cmd *Command, normalizedURL string, parsedURL *url.URL,
) *MessageResponse {
	checkResult, err := h.svc.CreateCheck(ctx, cmd.TeamID, normalizedURL)
	if err != nil {
		if errors.Is(err, ErrConnectionNotFound) {
			return ephemeral("This Slack workspace is not connected to SolidPing. Please reconnect the app.")
		}

		slog.ErrorContext(ctx, "Failed to create check",
			"url", normalizedURL,
			"team_id", cmd.TeamID,
			"error", err,
		)

		return ephemeral("Failed to create check: " + err.Error())
	}

	return &MessageResponse{
		ResponseType: ResponseTypeInChannel,
		Text:         fmt.Sprintf("Check created: %s for %s", checkResult.Slug, parsedURL.Host), // Fallback text
		Blocks: []Block{
			{
				Type: BlockTypeSection,
				Text: &Text{
					Type: BlockTypeMrkdwn,
					Text: fmt.Sprintf(":white_check_mark: *Check created:* `%s` for <%s|%s>",
						checkResult.Slug, normalizedURL, parsedURL.Host),
				},
			},
			{
				Type: BlockTypeContext,
				Elements: []any{
					ContextElement{
						Type: BlockTypeMrkdwn,
						Text: fmt.Sprintf("Created by <@%s>", cmd.UserID),
					},
				},
			},
		},
	}
}
