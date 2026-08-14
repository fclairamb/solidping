package telegramcb

import (
	"context"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
)

// handleCallbackQuery processes one inline-button press.
//
// EVERY exit path answers the callback query. Until it is answered Telegram
// leaves the button spinning in every client that can see the message, so an
// unanswered failure looks exactly like a hung bot — which, on a paging channel,
// is the one impression that must never be given.
func (h *Handler) handleCallbackQuery(ctx context.Context, query *telegram.CallbackQuery) {
	action, arg := telegram.ParseCallbackData(query.Data)
	if action != telegram.CallbackActionAck {
		h.log.InfoContext(ctx, "ignoring unknown telegram callback action",
			"action", action, "callbackId", query.ID)
		h.answer(ctx, query.ID, "")

		return
	}

	chatID := callbackChatID(query)
	if chatID == "" {
		h.log.WarnContext(ctx, "telegram callback carried no chat", "callbackId", query.ID)
		h.answer(ctx, query.ID, "Could not identify this chat.")

		return
	}

	orgs := h.linkedOrgs(ctx, chatID)
	if len(orgs) == 0 {
		// Same guard as the typed commands, and the same reason: an unlinked
		// chat must not learn whether the incident behind this button exists.
		h.log.InfoContext(ctx, "telegram ack callback from an unlinked chat", "chatId", chatID)
		h.answer(ctx, query.ID, "This chat is not connected to SolidPing.")

		return
	}

	// Route by the incident UID the button carries, across EVERY org this chat
	// is linked in — never by "whichever org sorts first". A button attached to
	// org B's alert used to dead-end on "that incident is no longer available"
	// whenever org A happened to be first, for an incident legitimately paged
	// into this very chat.
	org, incident := h.findIncidentAcrossOrgs(ctx, orgs, arg)
	if incident == nil {
		h.log.InfoContext(ctx, "telegram ack callback for an unknown incident",
			"chatId", chatID, "incidentUid", arg)
		h.answer(ctx, query.ID, "That incident is no longer available.")

		return
	}

	h.ackFromCallback(ctx, query, org, qualifyRefs(orgs), chatID, incident)
}

// ackFromCallback acknowledges the incident behind a pressed button and
// rewrites the alert it was attached to.
//
// org is the linked org the incident was actually found in, so the ack is
// credited to THAT org's contact owner and the audit trail names the right user
// row — not the row of whichever org happened to be listed first.
func (h *Handler) ackFromCallback(
	ctx context.Context, query *telegram.CallbackQuery,
	org *linkedOrg, qualify bool, chatID string, incident *models.Incident,
) {
	orgUID := org.orgUID

	// Idempotency, not an error: two people pressing the same button seconds
	// apart is the normal case during an outage. Report the current state and
	// still repaint the message, so the loser of the race also sees the truth.
	if incident.State == models.IncidentStateResolved {
		h.answer(ctx, query.ID, "Already resolved — nothing to acknowledge.")
		h.repaintAcked(ctx, query, org, qualify, incident)

		return
	}

	if incident.AcknowledgedAt != nil {
		h.answer(ctx, query.ID, "Already acknowledged.")
		h.repaintAcked(ctx, query, org, qualify, incident)

		return
	}

	if h.acker == nil {
		h.log.WarnContext(ctx, "telegram ack callback with no incident service wired",
			"chatId", chatID, "incidentUid", incident.UID)
		h.answer(ctx, query.ID, "Acknowledging is not available on this instance.")

		return
	}

	actor := telegram.ActorLabel(callbackActorName(query))

	acked, ackErr := h.acker.AcknowledgeIncidentFromTelegram(
		ctx, orgUID, incident.UID, org.contact.UserUID, actor,
	)
	if ackErr != nil {
		h.log.ErrorContext(ctx, "failed to acknowledge incident from a telegram button",
			"chatId", chatID, "incidentUid", incident.UID, "error", ackErr)
		h.answer(ctx, query.ID, "Could not acknowledge — try again.")

		return
	}

	h.log.InfoContext(ctx, "incident acknowledged from a telegram button",
		"chatId", chatID, "incidentUid", incident.UID, "orgUid", orgUID, "actor", actor)
	h.answer(ctx, query.ID, "✅ Acknowledged")
	h.repaintAcked(ctx, query, org, qualify, acked)
}

// repaintAcked rewrites the alert the button was attached to: the acknowledged
// body, and NO buttons.
//
// The edit is the durable half of the interaction — the toast lasts two seconds
// and only the presser sees it, while this is what the next person scrolling the
// chat reads. Removing the keyboard needs an explicit EMPTY keyboard: Telegram
// treats an absent reply_markup on an edit as "leave the buttons alone".
//
// Best-effort by contract: the acknowledgement itself is already committed, so a
// failed edit costs cosmetics, never correctness.
func (h *Handler) repaintAcked(
	ctx context.Context, query *telegram.CallbackQuery,
	org *linkedOrg, qualify bool, incident *models.Incident,
) {
	if h.sender == nil || query.Message == nil || query.Message.MessageID == 0 {
		return
	}

	chatID := callbackChatID(query)
	if chatID == "" {
		return
	}

	ackedAt := time.Now()
	if incident.AcknowledgedAt != nil {
		ackedAt = *incident.AcknowledgedAt
	}

	who := h.userLabel(ctx, incident.AcknowledgedBy)
	if who == "" {
		who = callbackActorName(query)
	}

	params := &telegram.AlertParams{
		State:       incidentStateLabel(incident),
		Number:      incident.Number,
		IncidentUID: incident.UID,
		CheckName:   h.checkName(ctx, org.orgUID, incident.CheckUID),
		Detail:      incidentDetailLine(incident),
		OrgSlug:     org.orgSlug,
		QualifyRef:  qualify,
		IncidentURL: h.incidentURL(org.orgSlug, incident.UID),
	}

	if err := h.sender.EditMessage(
		ctx, chatID, query.Message.MessageID,
		telegram.BuildAcknowledgedHTML(params, who, ackedAt),
		telegram.EmptyInlineKeyboard(),
	); err != nil {
		h.log.InfoContext(ctx, "could not rewrite the acknowledged telegram alert",
			"chatId", chatID, "incidentUid", incident.UID,
			"reason", telegram.FailureReason(err), "error", err)
	}
}

// incidentDetailLine is the one-line detail of an alert body: the incident
// title when it has one.
func incidentDetailLine(incident *models.Incident) string {
	if incident.Title != nil {
		return strings.TrimSpace(*incident.Title)
	}

	return ""
}

// callbackChatID is the chat the pressed button lives in.
func callbackChatID(query *telegram.CallbackQuery) string {
	if query.Message == nil || query.Message.Chat == nil || query.Message.Chat.ID == 0 {
		return ""
	}

	return telegram.ChatIDString(query.Message.Chat.ID)
}

// callbackActorName is the display name of whoever pressed the button. In a
// group this is very often NOT the person the chat is linked to, which is
// precisely why it is recorded separately from the credited user.
//
// The group case is UNREACHABLE today, and that is deliberate rather than dead
// code left behind: v1 refuses to link anything but a private chat
// (chatIsConnectable in handler.go), so no group chat can hold a verified
// contact and no group press gets past linkedContacts. The separate actor label
// is kept because it is what makes group routing additive when it lands —
// mapping a Telegram user id onto an org member is explicitly out of scope here.
func callbackActorName(query *telegram.CallbackQuery) string {
	if query.From == nil {
		return ""
	}

	if name := strings.TrimSpace(query.From.FirstName); name != "" {
		return name
	}

	return strings.TrimSpace(query.From.Username)
}

// answer closes the callback query with a toast, best-effort.
func (h *Handler) answer(ctx context.Context, callbackID, text string) {
	if h.sender == nil || callbackID == "" {
		return
	}

	if err := h.sender.AnswerCallback(ctx, callbackID, text); err != nil {
		h.log.InfoContext(ctx, "failed to answer a telegram callback query",
			"callbackId", callbackID, "reason", telegram.FailureReason(err), "error", err)
	}
}
