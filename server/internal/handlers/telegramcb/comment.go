package telegramcb

import (
	"context"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
)

// handleComment answers `/comment [#ref] <text>`.
//
// Same resolution UX as /ack, and for the same reason: an explicit `#42` wins,
// a bare command works when exactly one incident is open, and anything
// ambiguous LISTS the candidates instead of guessing. Commenting on the wrong
// incident is silent — the note lands where nobody handling the real outage
// will read it.
//
// The comment is attributed to the SolidPing user the chat is linked to (the
// only identity the platform knows), with the Telegram first name carried
// along so the timeline stays honest about who actually typed it in a group.
func (h *Handler) handleComment(
	ctx context.Context, msg *telegram.IncomingMessage, chatID, arg string,
) {
	orgs, ok := h.requireLinkedOrgs(ctx, chatID)
	if !ok {
		return
	}

	if h.commenter == nil {
		h.log.WarnContext(ctx, "telegram /comment with no commenter wired", "chatId", chatID)
		h.reply(ctx, chatID, telegram.BuildCommentUnavailableHTML())

		return
	}

	ref, text := splitCommentArg(arg)

	if text == "" {
		h.reply(ctx, chatID, telegram.BuildCommentUsageHTML())

		return
	}

	org, incident, resolved := h.resolveCommentTarget(ctx, chatID, orgs, ref)
	if !resolved {
		return
	}

	actor := telegram.ActorLabel(actorFirstName(msg))

	if _, err := h.commenter.AddCommentFromTelegram(
		ctx, org.orgUID, incident.UID, text, org.contact.UserUID, actor, chatID,
	); err != nil {
		h.log.ErrorContext(ctx, "failed to add incident comment from telegram",
			"chatId", chatID, "incidentUid", incident.UID, "error", err)
		h.reply(ctx, chatID, telegram.BuildCommentFailedHTML())

		return
	}

	h.reply(ctx, chatID, telegram.BuildCommentAddedHTML(
		refFor(qualifyRefs(orgs), org.orgSlug, incident.Number)))
}

// splitCommentArg separates an optional leading `#ref` from the comment body.
//
// The reference is only consumed when the first token actually starts with
// `#`; "the DNS is down" is entirely comment text, and never loses its first
// word to a reference parse.
func splitCommentArg(arg string) (string, string) {
	trimmed := strings.TrimSpace(arg)
	if !strings.HasPrefix(trimmed, "#") {
		return "", trimmed
	}

	head, tail, _ := strings.Cut(trimmed, " ")

	return head, strings.TrimSpace(tail)
}

// resolveCommentTarget picks the incident a /comment refers to, answering the
// chat and reporting false when it cannot. Mirrors resolveAckTarget, with one
// difference: a RESOLVED incident is still a legitimate comment target when
// named explicitly (post-mortem notes), so only the bare form is restricted to
// what is currently open.
func (h *Handler) resolveCommentTarget(
	ctx context.Context, chatID string, orgs []linkedOrg, ref string,
) (*linkedOrg, *models.Incident, bool) {
	if ref != "" {
		return h.resolveIncidentRef(ctx, chatID, orgs, "comment", ref)
	}

	open := make([]orgIncident, 0, len(orgs))

	for i := range orgs {
		for _, incident := range h.openIncidents(ctx, orgs[i].orgUID) {
			open = append(open, orgIncident{org: orgs[i], incident: incident})
		}
	}

	switch len(open) {
	case 0:
		h.reply(ctx, chatID, telegram.BuildNothingToCommentHTML())

		return nil, nil, false
	case 1:
		return &open[0].org, open[0].incident, true
	default:
		qualify := qualifyRefs(orgs)
		lines := make([]telegram.IncidentLine, 0, len(open))

		for i := range open {
			lines = append(lines, h.incidentLine(ctx, &open[i].org, qualify, open[i].incident))
		}

		h.reply(ctx, chatID, telegram.BuildCommentNeedsRefHTML(lines))

		return nil, nil, false
	}
}
