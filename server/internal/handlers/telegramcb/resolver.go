package telegramcb

import (
	"context"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
)

// linkedOrg is one organization a chat can act in: the verified contact that
// binds them, plus the org's identity resolved once so the command handlers do
// not each re-look-it-up.
type linkedOrg struct {
	contact *models.UserContact
	orgUID  string
	orgSlug string
}

// linkedOrgs returns the organizations this chat is linked in, oldest link
// first.
//
// One chat legitimately belongs to several orgs — each /start with a different
// org's connect token adds a row — and the ordering is the storage layer's
// (ORDER BY created_at, uid), so "first" is stable across calls rather than
// whatever the engine felt like returning.
//
// Deduplicated by organization: two SolidPing accounts in the SAME org can both
// link the same chat, and that must not make the org answer twice or count as
// two orgs for the purposes of qualifying references.
func (h *Handler) linkedOrgs(ctx context.Context, chatID string) []linkedOrg {
	contacts := h.linkedContacts(ctx, chatID)

	orgs := make([]linkedOrg, 0, len(contacts))
	seen := make(map[string]bool, len(contacts))

	for _, contact := range contacts {
		if seen[contact.OrganizationUID] {
			continue
		}

		seen[contact.OrganizationUID] = true

		orgs = append(orgs, linkedOrg{
			contact: contact,
			orgUID:  contact.OrganizationUID,
			orgSlug: h.orgSlug(ctx, contact.OrganizationUID),
		})
	}

	return orgs
}

// requireLinkedOrgs resolves the orgs a command may act in, or answers the
// "link your account" message and reports false.
//
// The answer is identical for every command and every chat, so an unlinked chat
// cannot use the bot to probe whether an org, a check or an incident exists.
func (h *Handler) requireLinkedOrgs(ctx context.Context, chatID string) ([]linkedOrg, bool) {
	orgs := h.linkedOrgs(ctx, chatID)
	if len(orgs) == 0 {
		h.log.InfoContext(ctx, "telegram command from an unlinked chat", "chatId", chatID)
		h.reply(ctx, chatID, telegram.BuildNotLinkedHTML())

		return nil, false
	}

	return orgs, true
}

// qualifyRefs reports whether this chat's messages must carry org-qualified
// references. Exactly the multi-org case: a single-org chat (the overwhelming
// majority) keeps the short "#42" and notices nothing.
func qualifyRefs(orgs []linkedOrg) bool {
	return len(orgs) > 1
}

// refFor renders an incident reference the way THIS chat has to read it.
func refFor(qualify bool, orgSlug string, number int64) string {
	if qualify {
		return telegram.QualifiedIncidentRef(orgSlug, number)
	}

	return telegram.IncidentRef(number)
}

// orgBySlug finds a linked org by its CURRENT slug, case-insensitively.
//
// Current slugs only: resolving through organization_previous_slugs aliases is
// deliberately out of scope for v1 (a renamed org's old reference simply reads
// as not-connected rather than silently routing somewhere else).
func orgBySlug(orgs []linkedOrg, slug string) *linkedOrg {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if slug == "" {
		return nil
	}

	for i := range orgs {
		if strings.EqualFold(orgs[i].orgSlug, slug) {
			return &orgs[i]
		}
	}

	return nil
}

// scopeToOrg narrows a read-only command to one linked org when the user named
// one ("/status acme"), or leaves the full set when they did not.
//
// A slug the chat is not linked to gets the same answer as a slug that does not
// exist at all — the bot must not become an oracle for which organizations
// exist on the instance.
func (h *Handler) scopeToOrg(
	ctx context.Context, chatID string, orgs []linkedOrg, arg string,
) ([]linkedOrg, bool) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return orgs, true
	}

	org := orgBySlug(orgs, arg)
	if org == nil {
		h.log.InfoContext(ctx, "telegram command scoped to an org this chat is not linked to",
			"chatId", chatID, "orgSlug", arg)
		h.reply(ctx, chatID, telegram.BuildNotConnectedToOrgHTML(arg))

		return nil, false
	}

	return []linkedOrg{*org}, true
}

// resolveIncidentRef turns a typed reference into (org, incident), answering the
// chat and reporting false whenever it cannot.
//
// The three shapes it has to get right:
//
//   - QUALIFIED ("#acme:42"): resolved in that org only. A slug the chat is not
//     linked to answers not-connected WITHOUT looking any incident up, so the
//     command cannot be used to probe another org's incident numbers.
//   - BARE in a single-org chat: today's behavior, one lookup, unchanged.
//   - BARE in a multi-org chat: looked up in every linked org. Exactly one hit
//     is acted on; several hits are NEVER guessed at — acking the wrong org's
//     #42 silences a page nobody is watching while the real outage stays
//     unclaimed, and it does so silently.
func (h *Handler) resolveIncidentRef(
	ctx context.Context, chatID string, orgs []linkedOrg, command, arg string,
) (*linkedOrg, *models.Incident, bool) {
	slug, number, valid := telegram.ParseIncidentRef(arg)
	if !valid {
		h.reply(ctx, chatID, telegram.BuildBadRefHTML(command))

		return nil, nil, false
	}

	if slug != "" {
		return h.resolveQualifiedRef(ctx, chatID, orgs, slug, number)
	}

	return h.resolveBareRef(ctx, chatID, orgs, command, number)
}

// resolveQualifiedRef handles "#acme:42".
func (h *Handler) resolveQualifiedRef(
	ctx context.Context, chatID string, orgs []linkedOrg, slug string, number int64,
) (*linkedOrg, *models.Incident, bool) {
	org := orgBySlug(orgs, slug)
	if org == nil {
		h.log.InfoContext(ctx, "telegram incident ref named an org this chat is not linked to",
			"chatId", chatID, "orgSlug", slug)
		h.reply(ctx, chatID, telegram.BuildNotConnectedToOrgHTML(slug))

		return nil, nil, false
	}

	incident := h.incidentByNumber(ctx, org.orgUID, number)
	if incident == nil {
		h.reply(ctx, chatID,
			telegram.BuildIncidentNotFoundHTML(telegram.QualifiedIncidentRef(org.orgSlug, number)))

		return nil, nil, false
	}

	return org, incident, true
}

// resolveBareRef handles "#42" — unambiguous in a single-org chat, and resolved
// across every linked org otherwise.
func (h *Handler) resolveBareRef(
	ctx context.Context, chatID string, orgs []linkedOrg, command string, number int64,
) (*linkedOrg, *models.Incident, bool) {
	matches := make([]orgIncident, 0, len(orgs))

	for i := range orgs {
		if incident := h.incidentByNumber(ctx, orgs[i].orgUID, number); incident != nil {
			matches = append(matches, orgIncident{org: orgs[i], incident: incident})
		}
	}

	switch len(matches) {
	case 0:
		h.reply(ctx, chatID, telegram.BuildIncidentNotFoundHTML(telegram.IncidentRef(number)))

		return nil, nil, false
	case 1:
		return &matches[0].org, matches[0].incident, true
	default:
		h.log.InfoContext(ctx, "ambiguous telegram incident ref across linked orgs",
			"chatId", chatID, "number", number, "candidates", len(matches))
		h.reply(ctx, chatID, telegram.BuildAmbiguousRefHTML(command, h.candidateLines(ctx, matches)))

		return nil, nil, false
	}
}

// orgIncident pairs an incident with the linked org it was found in.
type orgIncident struct {
	org      linkedOrg
	incident *models.Incident
}

// candidateLines renders the disambiguation list. Always qualified: the whole
// point of the list is that the short reference could not tell them apart.
func (h *Handler) candidateLines(ctx context.Context, matches []orgIncident) []telegram.IncidentLine {
	lines := make([]telegram.IncidentLine, 0, len(matches))

	for i := range matches {
		lines = append(lines, h.incidentLine(ctx, &matches[i].org, true, matches[i].incident))
	}

	return lines
}

// findIncidentAcrossOrgs locates an incident UID in whichever of the chat's
// linked orgs owns it.
//
// Incident UIDs are globally unique, so at most one org can match and the loop
// cannot pick the "wrong" one. No match in any linked org means the incident was
// never paged to this chat — the caller answers exactly as it always has, which
// still reveals nothing.
func (h *Handler) findIncidentAcrossOrgs(
	ctx context.Context, orgs []linkedOrg, incidentUID string,
) (*linkedOrg, *models.Incident) {
	for i := range orgs {
		incident, err := h.db.GetIncident(ctx, orgs[i].orgUID, incidentUID)
		if err == nil && incident != nil {
			return &orgs[i], incident
		}
	}

	return nil, nil
}
