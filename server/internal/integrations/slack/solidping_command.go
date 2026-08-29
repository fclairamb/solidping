package slack

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// solidpingKnownSubcommands is the single source of truth for what
// `/solidping <word>` accepts. handleSolidpingCommand consults it before
// dispatching, and TestSolidpingUsageHintSubcommandsHaveHandlers
// (solidping_command_test.go) consults the exact same map when checking the
// manifests' usage hint — so a subcommand named in a manifest hint with no
// entry here fails a test instead of silently answering "Unknown command" in
// production, which is the failure this whole spec exists to fix.
//
// "" covers bare `/solidping` (empty text), which the mention parser already
// maps to help. "checks" and "results" are the underlying app_mention
// commands `list`/`create` alias to — kept dispatchable directly too, for
// parity with the app_mention transport, even though the short usage hint in
// the manifests only advertises the friendlier aliases.
var solidpingKnownSubcommands = map[string]bool{
	"":           true,
	cmdHelp:      true,
	cmdConfig:    true,
	cmdIncidents: true,
	cmdChecks:    true,
	"results":    true,
	"list":       true,
	"create":     true,
	"check":      true,
	"comment":    true,
}

// handleSolidpingCommand implements `/solidping <subcommand>`, the single
// slash command that replaces the standalone `/check` and `/comment`
// commands and every subcommand `@solidping` already understood.
//
// `check` and `comment` route to their own handlers (same ones the retired
// standalone commands used — only the entry point moved). Everything else
// routes through the app_mention parser and router, via
// dispatchSolidpingViaMentionRouter, which adapts their in-channel reply into
// an ephemeral one.
func (h *Handler) handleSolidpingCommand(ctx context.Context, cmd *Command) (*MessageResponse, error) {
	text := preprocessCommandText(cmd.Text)
	word, rest := splitFirstWord(text)
	lower := strings.ToLower(word)

	if !solidpingKnownSubcommands[lower] {
		return ephemeral(fmt.Sprintf(
			"Unknown command: `%s`. Type `/solidping help` for available commands.", word,
		)), nil
	}

	switch lower {
	case "check":
		return h.handleCheckCommand(ctx, &Command{
			TeamID:    cmd.TeamID,
			ChannelID: cmd.ChannelID,
			UserID:    cmd.UserID,
			ThreadTS:  cmd.ThreadTS,
			Text:      rest,
		})
	case "comment":
		return h.handleCommentCommand(ctx, &Command{
			TeamID:    cmd.TeamID,
			ChannelID: cmd.ChannelID,
			UserID:    cmd.UserID,
			UserName:  cmd.UserName,
			Text:      rest,
		})
	default:
		return h.dispatchSolidpingViaMentionRouter(ctx, cmd)
	}
}

// dispatchSolidpingViaMentionRouter parses cmd.Text with the same parser the
// app_mention transport uses and dispatches through handleMentionCommand, the
// shared subcommand router. Slack's guidelines ask a slash command to answer
// ephemerally ("minimize disruption"), unlike a mention — which answers
// in-channel because the mention itself was already public — so a fresh
// Handler is built with `respond` set to capture the router's reply instead
// of letting it post via chat.postMessage.
func (h *Handler) dispatchSolidpingViaMentionRouter(ctx context.Context, cmd *Command) (*MessageResponse, error) {
	parsed := ParseMentionText(cmd.Text)
	applySolidpingAliases(parsed)

	event := &Event{
		TeamID: cmd.TeamID,
		Event: EventPayload{
			Channel: cmd.ChannelID,
			User:    cmd.UserID,
		},
	}

	var captured *MessageResponse

	responder := &Handler{
		svc: h.svc,
		respond: func(msg *MessageResponse) error {
			captured = msg

			return nil
		},
	}

	if err := responder.handleMentionCommand(ctx, event, parsed); err != nil {
		slog.ErrorContext(ctx, "Failed to handle /solidping command",
			"subcommand", parsed.Command, "error", err)

		return ephemeral("Sorry, something went wrong handling that command."), nil
	}

	if captured == nil {
		// Every leaf of handleMentionCommand replies via sendMentionResponse
		// or sendMentionError, both of which now go through h.respond above —
		// this is a defensive fallback, not an expected path.
		captured = ephemeral("Sorry, something went wrong handling that command.")
	}

	captured.ResponseType = ResponseTypeEphemeral

	return captured, nil
}

// applySolidpingAliases rewrites the two /solidping-only shorthands onto the
// app_mention commands they stand for, so the shared router never has to know
// they exist: `list` -> `checks list`, `create` -> `checks add`.
func applySolidpingAliases(cmd *ParsedCommand) {
	switch cmd.Command {
	case "list":
		cmd.Command = cmdChecks
		cmd.Subcommand = subList
	case "create":
		cmd.Command = cmdChecks
		cmd.Subcommand = subAdd
	}
}

// splitFirstWord splits already-trimmed command text into its first
// whitespace-delimited word and the (trimmed) remainder. Used instead of the
// mention parser's quote-aware tokenizer for `check`/`comment`, whose own
// handlers parse the remainder themselves (handleCheckCommand normalizes a
// bare host into a URL; handleCommentCommand's parseCommentArgs looks for a
// leading `#N`) and must see it verbatim, not rebuilt from re-joined tokens.
func splitFirstWord(text string) (word, rest string) {
	if text == "" {
		return "", ""
	}

	head, tail, found := strings.Cut(text, " ")
	if !found {
		return head, ""
	}

	return head, strings.TrimSpace(tail)
}

// legacyMovedNotice is the ephemeral reply for the retired standalone
// `/check` and `/comment` commands. They are kept registered in DispatchCommand
// (not deleted) because a workspace that installed the old manifest keeps
// those commands registered with Slack until it re-authorizes — deleting the
// arms would strand those installs on "Unknown command".
func legacyMovedNotice(oldCommand, newUsage string) *MessageResponse {
	return ephemeral(fmt.Sprintf(
		"`%s` has moved to `%s` — the standalone `%s` command is going away.",
		oldCommand, newUsage, oldCommand,
	))
}
