package telegramcb

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/telegram"
)

// maxListedIncidents bounds an /incidents listing. Each row ships as its own
// message (one inline keyboard per message is a Telegram constraint), so an org
// with a hundred simultaneous failures would otherwise carpet-bomb the chat and
// hit the per-chat rate limit — losing the very list it was trying to send.
const maxListedIncidents = 10

// linkedContacts returns the verified Telegram contacts bound to a chat.
//
// Unverified rows are dropped on purpose: an unverified telegram contact means
// the link was revoked or the bot was blocked, and such a chat must not be able
// to read incidents any more than it can be paged.
func (h *Handler) linkedContacts(ctx context.Context, chatID string) []*models.UserContact {
	contacts, err := h.db.ListUserContactsByTypeValue(ctx, models.UserContactTypeTelegram, chatID)
	if err != nil {
		h.log.WarnContext(ctx, "failed to list telegram contacts for chat", "chatId", chatID, "error", err)

		return nil
	}

	live := make([]*models.UserContact, 0, len(contacts))

	for _, contact := range contacts {
		if contact.VerifiedAt != nil {
			live = append(live, contact)
		}
	}

	return live
}

// handleHelp answers /help.
func (h *Handler) handleHelp(ctx context.Context, chatID string) {
	if _, ok := h.requireLinkedOrgs(ctx, chatID); !ok {
		return
	}

	h.reply(ctx, chatID, telegram.BuildHelpHTML())
}

// handleStatus answers /status [org] with one line of org health.
//
// /status is READ-ONLY, which is what makes fanning it out across every linked
// org safe: a chat linked in three orgs gets three lines rather than one line
// about whichever org happened to sort first. An optional org slug narrows it.
func (h *Handler) handleStatus(ctx context.Context, chatID, arg string) {
	orgs, ok := h.requireLinkedOrgs(ctx, chatID)
	if !ok {
		return
	}

	targets, scoped := h.scopeToOrg(ctx, chatID, orgs, arg)
	if !scoped {
		return
	}

	for i := range targets {
		h.reply(ctx, chatID, telegram.BuildStatusHTML(h.statusView(ctx, &targets[i])))
	}
}

// statusView counts one org's health.
func (h *Handler) statusView(ctx context.Context, org *linkedOrg) *telegram.StatusView {
	incidents := h.openIncidents(ctx, org.orgUID)

	total := 0
	if checks, _, err := h.db.ListChecks(ctx, org.orgUID, &models.ListChecksFilter{}); err == nil {
		for _, check := range checks {
			if check.Enabled {
				total++
			}
		}
	} else {
		h.log.WarnContext(ctx, "failed to count checks for /status", "orgUid", org.orgUID, "error", err)
	}

	down := make(map[string]bool, len(incidents))
	for _, incident := range incidents {
		down[incident.CheckUID] = true
	}

	return &telegram.StatusView{
		OrgSlug:       org.orgSlug,
		TotalChecks:   total,
		ChecksDown:    len(down),
		OpenIncidents: len(incidents),
	}
}

// handleIncidents answers /incidents [org]: per org, a header then one message
// per open incident so each can carry its own Acknowledge button.
//
// The maxListedIncidents cap applies to the TOTAL across orgs, not per org: the
// cap exists because each row is its own message and a hundred of them would hit
// the per-chat rate limit — a budget that a multi-org chat shares rather than
// multiplies.
func (h *Handler) handleIncidents(ctx context.Context, chatID, arg string) {
	orgs, ok := h.requireLinkedOrgs(ctx, chatID)
	if !ok {
		return
	}

	targets, scoped := h.scopeToOrg(ctx, chatID, orgs, arg)
	if !scoped {
		return
	}

	qualify := qualifyRefs(orgs)
	budget := maxListedIncidents

	for i := range targets {
		budget = h.listOrgIncidents(ctx, chatID, &targets[i], qualify, budget)
	}
}

// listOrgIncidents sends one org's section of an /incidents answer and returns
// the remaining row budget.
func (h *Handler) listOrgIncidents(
	ctx context.Context, chatID string, org *linkedOrg, qualify bool, budget int,
) int {
	incidents := h.openIncidents(ctx, org.orgUID)

	if len(incidents) == 0 {
		h.reply(ctx, chatID, telegram.BuildNoOpenIncidentsHTML(org.orgSlug))

		return budget
	}

	// The header always reports the TRUE count, even when the budget truncates
	// the rows below it — a listing that silently under-reports how much is
	// broken is worse than one that is visibly cut short.
	h.reply(ctx, chatID, telegram.BuildIncidentsHTML(org.orgSlug, len(incidents)))

	if len(incidents) > budget {
		incidents = incidents[:max(budget, 0)]
	}

	for _, incident := range incidents {
		line := h.incidentLine(ctx, org, qualify, incident)
		keyboard := telegram.IncidentKeyboard(incident.UID, line.IncidentURL, !line.Acked)

		h.replyWithKeyboard(ctx, chatID, telegram.BuildIncidentLineHTML(&line), keyboard)
	}

	return budget - len(incidents)
}

// handleIncidentDetail answers /incident <#ref>.
func (h *Handler) handleIncidentDetail(ctx context.Context, chatID, arg string) {
	orgs, ok := h.requireLinkedOrgs(ctx, chatID)
	if !ok {
		return
	}

	org, incident, resolved := h.resolveIncidentRef(ctx, chatID, orgs, "incident", arg)
	if !resolved {
		return
	}

	view := h.incidentDetailView(ctx, org, qualifyRefs(orgs), incident)
	canAck := incident.State != models.IncidentStateResolved && incident.AcknowledgedAt == nil
	keyboard := telegram.IncidentKeyboard(incident.UID, view.IncidentURL, canAck)

	h.replyWithKeyboard(ctx, chatID, telegram.BuildIncidentDetailHTML(view), keyboard)
}

// handleAck answers /ack [#ref].
//
// With no argument it acks the single open incident — across every linked org,
// because "the only thing that is broken" is a fact about the chat, not about
// whichever org sorts first. When several are open it LISTS them instead of
// guessing: acking the wrong incident is silent — the page stops and nobody
// notices the real one is still unclaimed.
func (h *Handler) handleAck(
	ctx context.Context, msg *telegram.IncomingMessage, chatID, arg string,
) {
	orgs, ok := h.requireLinkedOrgs(ctx, chatID)
	if !ok {
		return
	}

	org, incident, resolved := h.resolveAckTarget(ctx, chatID, orgs, arg)
	if !resolved {
		return
	}

	actor := telegram.ActorLabel(actorFirstName(msg))

	acked, err := h.acknowledge(ctx, org.orgUID, incident, org.contact.UserUID, actor)
	if err != nil {
		h.log.ErrorContext(ctx, "failed to acknowledge incident from telegram",
			"chatId", chatID, "incidentUid", incident.UID, "error", err)
		h.reply(ctx, chatID, telegram.BuildAckFailedHTML())

		return
	}

	h.reply(ctx, chatID, h.ackOutcomeHTML(ctx, org, qualifyRefs(orgs), acked, incident))
}

// resolveAckTarget picks the incident a /ack refers to, answering the chat and
// returning false when it cannot.
func (h *Handler) resolveAckTarget(
	ctx context.Context, chatID string, orgs []linkedOrg, arg string,
) (*linkedOrg, *models.Incident, bool) {
	if strings.TrimSpace(arg) != "" {
		return h.resolveIncidentRef(ctx, chatID, orgs, "ack", arg)
	}

	open := make([]orgIncident, 0, len(orgs))

	for i := range orgs {
		for _, incident := range h.openIncidents(ctx, orgs[i].orgUID) {
			open = append(open, orgIncident{org: orgs[i], incident: incident})
		}
	}

	switch len(open) {
	case 0:
		h.reply(ctx, chatID, telegram.BuildNothingToAckHTML())

		return nil, nil, false
	case 1:
		return &open[0].org, open[0].incident, true
	default:
		qualify := qualifyRefs(orgs)
		lines := make([]telegram.IncidentLine, 0, len(open))

		for i := range open {
			lines = append(lines, h.incidentLine(ctx, &open[i].org, qualify, open[i].incident))
		}

		h.reply(ctx, chatID, telegram.BuildAckNeedsRefHTML(lines))

		return nil, nil, false
	}
}

// acknowledge runs the ack through the incident service. A resolved incident is
// never acked — the caller renders the "already resolved" answer from the
// unchanged incident it gets back.
func (h *Handler) acknowledge(
	ctx context.Context, orgUID string, incident *models.Incident, userUID, actor string,
) (*models.Incident, error) {
	if h.acker == nil || incident.State == models.IncidentStateResolved || incident.AcknowledgedAt != nil {
		return incident, nil
	}

	return h.acker.AcknowledgeIncidentFromTelegram(ctx, orgUID, incident.UID, userUID, actor)
}

// ackOutcomeHTML renders the answer for an ack attempt, covering the three
// idempotent outcomes: freshly acked, already acked, already resolved.
//
// The reference is rendered for THIS chat: in a multi-org chat the confirmation
// has to name which org's #42 was just taken, or it confirms nothing.
func (h *Handler) ackOutcomeHTML(
	ctx context.Context, org *linkedOrg, qualify bool, acked, before *models.Incident,
) string {
	if before.State == models.IncidentStateResolved {
		return telegram.BuildIncidentResolvedHTML(refFor(qualify, org.orgSlug, before.Number))
	}

	if before.AcknowledgedAt != nil {
		return telegram.BuildAlreadyAckedHTML(
			refFor(qualify, org.orgSlug, before.Number),
			h.userLabel(ctx, before.AcknowledgedBy), *before.AcknowledgedAt,
		)
	}

	if acked == nil || h.acker == nil {
		return telegram.BuildAckFailedHTML()
	}

	return telegram.BuildAckedHTML(
		refFor(qualify, org.orgSlug, acked.Number),
		h.checkName(ctx, org.orgUID, acked.CheckUID),
	)
}

// openIncidents lists the org's open, non-suppressed incidents, oldest first —
// the order an on-call person triages in.
func (h *Handler) openIncidents(ctx context.Context, orgUID string) []*models.Incident {
	incidents, _, err := h.db.ListIncidents(ctx, &models.ListIncidentsFilter{
		OrganizationUID: orgUID,
		States:          []models.IncidentState{models.IncidentStateActive},
		HideSuppressed:  true,
	})
	if err != nil {
		h.log.WarnContext(ctx, "failed to list open incidents", "orgUid", orgUID, "error", err)

		return nil
	}

	// The DB hands these back newest-first (the dashboard's order). A chat
	// listing is triaged top-down, so the longest-running incident — the one
	// nobody has taken — has to be the first thing read, not the last.
	sort.SliceStable(incidents, func(i, j int) bool {
		return incidents[i].StartedAt.Before(incidents[j].StartedAt)
	})

	return incidents
}

// incidentByNumber resolves a `#42` reference, or nil.
func (h *Handler) incidentByNumber(ctx context.Context, orgUID string, number int64) *models.Incident {
	incident, err := h.db.GetIncidentByNumber(ctx, orgUID, number)
	if err != nil || incident == nil {
		return nil
	}

	return incident
}

// incidentLine builds one listing row.
func (h *Handler) incidentLine(
	ctx context.Context, org *linkedOrg, qualify bool, incident *models.Incident,
) telegram.IncidentLine {
	return telegram.IncidentLine{
		Number:      incident.Number,
		UID:         incident.UID,
		OrgSlug:     org.orgSlug,
		QualifyRef:  qualify,
		CheckName:   h.checkName(ctx, org.orgUID, incident.CheckUID),
		State:       incidentStateLabel(incident),
		OpenFor:     time.Since(incident.StartedAt),
		Acked:       incident.AcknowledgedAt != nil,
		AckedBy:     h.userLabel(ctx, incident.AcknowledgedBy),
		IncidentURL: h.incidentURL(org.orgSlug, incident.UID),
	}
}

// incidentDetailView assembles the /incident answer.
func (h *Handler) incidentDetailView(
	ctx context.Context, org *linkedOrg, qualify bool, incident *models.Incident,
) *telegram.IncidentDetailView {
	openFor := time.Since(incident.StartedAt)
	if incident.ResolvedAt != nil {
		openFor = incident.ResolvedAt.Sub(incident.StartedAt)
	}

	return &telegram.IncidentDetailView{
		Number:      incident.Number,
		OrgSlug:     org.orgSlug,
		QualifyRef:  qualify,
		CheckName:   h.checkName(ctx, org.orgUID, incident.CheckUID),
		State:       incidentStateLabel(incident),
		OpenFor:     openFor,
		Regions:     incidentRegions(incident),
		LastError:   incidentFailureReason(incident),
		AckedBy:     h.userLabel(ctx, incident.AcknowledgedBy),
		AckedAt:     incident.AcknowledgedAt,
		ResolvedAt:  incident.ResolvedAt,
		IncidentURL: h.incidentURL(org.orgSlug, incident.UID),
	}
}

// incidentStateLabel mirrors the escalation job's labels so an incident reads
// the same in a /incidents listing and in the alert that paged for it.
func incidentStateLabel(incident *models.Incident) string {
	switch {
	case incident.State == models.IncidentStateResolved:
		return telegram.StateResolved
	case incident.EscalatedAt != nil:
		return telegram.StateEscalated
	default:
		return telegram.StateDown
	}
}

// incidentRegions returns the regions the incident is failing in, from the
// incident's own region plus the first-failure snapshot.
func incidentRegions(incident *models.Incident) []string {
	seen := make(map[string]bool, 2)
	regions := make([]string, 0, 2)

	add := func(region string) {
		region = strings.TrimSpace(region)
		if region == "" || seen[region] {
			return
		}

		seen[region] = true
		regions = append(regions, region)
	}

	if incident.Region != nil {
		add(*incident.Region)
	}

	if snapshot, ok := incident.Details["first_result"].(map[string]any); ok {
		if region, ok := snapshot["region"].(string); ok {
			add(region)
		}
	}

	return regions
}

// incidentFailureReason reads the cause recorded when the incident opened —
// the same `failure_reason` key the Slack formatters render.
func incidentFailureReason(incident *models.Incident) string {
	if incident.Details == nil {
		return ""
	}

	reason, _ := incident.Details["failure_reason"].(string)

	return reason
}

// checkName resolves a check's display name, degrading to "" rather than
// failing a read-only command over a deleted check.
func (h *Handler) checkName(ctx context.Context, orgUID, checkUID string) string {
	check, err := h.db.GetCheck(ctx, orgUID, checkUID)
	if err != nil || check == nil {
		return ""
	}

	if check.Name != nil && *check.Name != "" {
		return *check.Name
	}

	if check.Slug != nil {
		return *check.Slug
	}

	return ""
}

// userLabel renders who acknowledged an incident. AcknowledgedBy holds a user
// UID, which says nothing to a human in a chat, so it is resolved to the name
// (then the email) and falls back to the raw UID only when the user is gone.
func (h *Handler) userLabel(ctx context.Context, userUID *string) string {
	if userUID == nil || *userUID == "" {
		return ""
	}

	user, err := h.db.GetUser(ctx, *userUID)
	if err != nil || user == nil {
		return *userUID
	}

	if user.Name != "" {
		return user.Name
	}

	if user.Email != "" {
		return user.Email
	}

	return *userUID
}

// incidentURL builds the dashboard deep link, or "" when the instance has no
// base URL configured.
func (h *Handler) incidentURL(orgSlug, incidentUID string) string {
	if h.cfg == nil {
		return ""
	}

	return telegram.IncidentURL(h.cfg.Server.BaseURL, orgSlug, incidentUID)
}

// actorFirstName is the display name of whoever typed the command.
func actorFirstName(msg *telegram.IncomingMessage) string {
	if msg == nil || msg.From == nil {
		return ""
	}

	if name := strings.TrimSpace(msg.From.FirstName); name != "" {
		return name
	}

	return strings.TrimSpace(msg.From.Username)
}

// replyWithKeyboard sends a best-effort in-chat message carrying buttons.
func (h *Handler) replyWithKeyboard(
	ctx context.Context, chatID, html string, keyboard *telegram.InlineKeyboard,
) {
	if h.sender == nil {
		return
	}

	if err := h.sender.SendKeyboard(ctx, chatID, html, keyboard); err != nil {
		h.log.WarnContext(ctx, "failed to send telegram reply",
			"chatId", chatID, "reason", telegram.FailureReason(err), "error", err)
	}
}
