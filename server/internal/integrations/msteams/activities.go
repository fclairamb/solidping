package msteams

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// DispatchActivity is the transport-agnostic entry point for an inbound Bot
// Framework activity, mirroring slack.DispatchEvent. The HTTP messaging
// endpoint is the only transport today (Bot Framework has no Socket-Mode
// equivalent), but keeping dispatch separate from the handler keeps the
// command tests free of HTTP plumbing.
func DispatchActivity(ctx context.Context, svc *Service, activity *Activity) error {
	slog.InfoContext(ctx, "Handling Microsoft Teams activity",
		"activity_type", activity.Type,
		"action", activity.Action,
		"tenant_id", activity.TenantID(),
		"conversation_id", activity.ConversationID(),
	)

	dispatcher := &Handler{svc: svc}

	switch activity.Type {
	case ActivityTypeInstallationUpdate:
		return dispatcher.handleInstallationUpdate(ctx, activity)
	case ActivityTypeConversationUpdate:
		return dispatcher.handleConversationUpdate(ctx, activity)
	case ActivityTypeMessage:
		return dispatcher.handleMessage(ctx, activity)
	default:
		slog.DebugContext(ctx, "Unhandled Microsoft Teams activity type", "type", activity.Type)

		return nil
	}
}

// handleInstallationUpdate handles app install/uninstall.
//
// Teams also emits `add-upgrade` / `remove-upgrade` around an app version
// upgrade; only a plain `remove` tears the connection down, so an upgrade
// never looks like an uninstall.
func (h *Handler) handleInstallationUpdate(ctx context.Context, activity *Activity) error {
	switch activity.Action {
	case InstallActionRemove:
		return h.svc.HandleUninstall(ctx, activity.TenantID())
	case InstallActionAdd:
		if _, err := h.svc.HandleInstall(ctx, activity); err != nil {
			if errors.Is(err, ErrTenantNotLinked) {
				// Not an error condition — the admin simply has not linked
				// the tenant yet. Tell them how, in the channel.
				return h.replyTenantError(ctx, activity, err)
			}

			return fmt.Errorf("handling Teams install: %w", err)
		}

		return h.replyInstallWelcome(ctx, activity)
	default:
		slog.DebugContext(ctx, "Ignoring installationUpdate action", "action", activity.Action)

		return nil
	}
}

// handleConversationUpdate captures a conversation reference when the bot
// itself is added to a team/channel — the Teams analogue of Slack's
// member_joined_channel auto-configure.
func (h *Handler) handleConversationUpdate(ctx context.Context, activity *Activity) error {
	if !h.botWasAdded(ctx, activity) {
		return nil
	}

	if _, err := h.svc.HandleInstall(ctx, activity); err != nil {
		if errors.Is(err, ErrTenantNotLinked) {
			return h.replyTenantError(ctx, activity, err)
		}

		return fmt.Errorf("registering Teams conversation: %w", err)
	}

	return h.replyInstallWelcome(ctx, activity)
}

// botWasAdded reports whether this conversationUpdate added *us*, not a human
// member. The activity's Recipient is always the bot, so comparing it against
// membersAdded is a self-check that needs no stored bot id.
func (h *Handler) botWasAdded(_ context.Context, activity *Activity) bool {
	if activity.Recipient == nil || activity.Recipient.ID == "" {
		return false
	}

	for i := range activity.MembersAdded {
		if activity.MembersAdded[i].ID == activity.Recipient.ID {
			return true
		}
	}

	return false
}

// handleMessage turns an @mention into a command. Messages the bot itself
// posted are ignored so a reply can never re-trigger the parser.
func (h *Handler) handleMessage(ctx context.Context, activity *Activity) error {
	if activity.Recipient != nil && activity.From != nil &&
		activity.From.ID == activity.Recipient.ID {
		return nil
	}

	// Best-effort: keep the conversation reference and service URL fresh, so
	// a channel the bot is talked to in is addressable for notifications even
	// if its conversationUpdate was missed.
	if err := h.svc.RegisterConversation(ctx, activity); err != nil &&
		!errors.Is(err, ErrTenantNotLinked) && !errors.Is(err, ErrNoTenantID) {
		slog.WarnContext(ctx, "Failed to refresh Teams conversation reference",
			"tenant_id", activity.TenantID(), "error", err)
	}

	cmd := ParseMentionText(activity.Text)

	slog.InfoContext(ctx, "Parsed Microsoft Teams command",
		"command", cmd.Command,
		"subcommand", cmd.Subcommand,
		"args", cmd.Args,
		"flags", cmd.Flags,
	)

	return h.handleMentionCommand(ctx, activity, cmd)
}

// replyInstallWelcome greets the channel after a successful install and
// states what happens next.
func (h *Handler) replyInstallWelcome(ctx context.Context, activity *Activity) error {
	name := activity.ConversationName()
	if name == "" {
		name = "this channel"
	}

	body := strings.Join([]string{
		"I'll post incident notifications here.",
		"Type `" + mentionPrefix + " help` to see what I can do, " +
			"or `" + mentionPrefix + " config default-channel` to make " + name +
			" the default notification channel.",
	}, "\n\n")

	return h.replyCard(ctx, activity, "SolidPing is ready", SimpleCard("👋 SolidPing is ready!", body, CardColorGood))
}
