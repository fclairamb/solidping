package slack

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// commentUsage is shown for `/comment` with no text, and alongside every
// resolution failure, so the user never has to guess the shape.
const commentUsage = "*Usage:* `/comment [#N] <text>`\n\n" +
	"Examples:\n• `/comment I think the central DNS is down`\n• `/comment #42 restarting the pod`"

// maxCommentCandidates bounds the disambiguation list. More than a handful of
// simultaneously-tracked incidents in one channel and the list stops being a
// help — the user should name the incident.
const maxCommentCandidates = 10

// handleCommentCommand implements `/comment [#N] <text>`.
//
// Why a slash command and not a thread reply: Slack's client intercepts any
// message starting with `/`, so the text never posts. And a registered slash
// command's payload does NOT carry `thread_ts`, so the command itself cannot
// tell which thread it was typed in — the incident has to be resolved from the
// CHANNEL, or named explicitly.
//
// Resolution order, and it matters:
//  1. An explicit `#N` always wins. Someone who names the incident gets that
//     incident or an error, never a silent substitution.
//  2. Otherwise the channel's tracked incident threads are listed. Exactly one
//     ACTIVE incident is used; zero or several are answered with an error that
//     lists the candidates. Commenting on the wrong incident is silent — the
//     note lands where nobody handling the real outage will read it.
func (h *Handler) handleCommentCommand(ctx context.Context, cmd *Command) (*MessageResponse, error) {
	ref, text := parseCommentArgs(cmd.Text)

	if text == "" {
		return ephemeral(commentUsage), nil
	}

	conn, err := h.svc.GetConnectionByTeamID(ctx, cmd.TeamID)
	if err != nil || conn == nil {
		slog.WarnContext(ctx, "/comment from an unconnected workspace",
			"team_id", cmd.TeamID, "error", err)

		return ephemeral("This Slack workspace is not connected to SolidPing. Please reconnect the app."), nil
	}

	incident, resolveErr := h.resolveCommentIncident(ctx, conn.OrganizationUID, cmd, ref)
	if resolveErr != nil {
		return ephemeral(resolveErr.Error()), nil
	}

	displayName := h.resolveSlackUserName(ctx, cmd.TeamID, cmd.UserID)
	if displayName == "" {
		displayName = cmd.UserName
	}

	if _, err := h.svc.incidentsService.AddCommentFromSlackCommand(
		ctx, conn.OrganizationUID, incident.UID, text, cmd.UserID, displayName, cmd.TeamID,
	); err != nil {
		slog.ErrorContext(ctx, "Failed to add Slack /comment",
			"incident_uid", incident.UID, "error", err)

		return ephemeral("Could not add the comment: " + err.Error()), nil
	}

	return ephemeral(fmt.Sprintf(
		":speech_balloon: Comment added to %s.", incidentLabel(incident),
	)), nil
}

// parseCommentArgs splits "#42 restarting the pod" into ("42", "restarting the
// pod"). A leading token is only treated as a reference when it is `#` followed
// by digits — "#1 is broken" names incident 1, while "#ops please look" is just
// text, because "ops" is not a number.
func parseCommentArgs(raw string) (string, string) {
	text := strings.TrimSpace(raw)
	if !strings.HasPrefix(text, "#") {
		return "", text
	}

	rest := text[1:]

	head, tail, _ := strings.Cut(rest, " ")
	if head == "" {
		return "", text
	}

	if _, err := strconv.ParseInt(head, 10, 64); err != nil {
		return "", text
	}

	return head, strings.TrimSpace(tail)
}

// resolveCommentIncident picks the incident a `/comment` refers to. The error
// it returns is user-facing text, already formatted for an ephemeral reply.
func (h *Handler) resolveCommentIncident(
	ctx context.Context, orgUID string, cmd *Command, ref string,
) (*models.Incident, error) {
	if ref != "" {
		return h.incidentByRef(ctx, orgUID, ref)
	}

	candidates := h.activeChannelIncidents(ctx, orgUID, cmd.TeamID, cmd.ChannelID)

	switch len(candidates) {
	case 0:
		//nolint:err113 // user-facing message, not a sentinel callers branch on.
		return nil, fmt.Errorf(
			"No active SolidPing incident is tracked in this channel. "+
				"Name one explicitly:\n%s", commentUsage)
	case 1:
		return candidates[0], nil
	default:
		//nolint:err113 // user-facing message, not a sentinel callers branch on.
		return nil, fmt.Errorf(
			"Several active incidents are tracked in this channel — say which one:\n%s\n\n%s",
			strings.Join(candidateLines(candidates), "\n"), commentUsage)
	}
}

// incidentByRef resolves an explicit `#N`.
func (h *Handler) incidentByRef(ctx context.Context, orgUID, ref string) (*models.Incident, error) {
	number, err := strconv.ParseInt(ref, 10, 64)
	if err != nil {
		//nolint:err113 // user-facing message.
		return nil, fmt.Errorf("`#%s` is not an incident number.\n%s", ref, commentUsage)
	}

	incident, err := h.svc.db.GetIncidentByNumber(ctx, orgUID, number)
	if err != nil || incident == nil {
		//nolint:err113 // user-facing message.
		return nil, fmt.Errorf("Incident `#%s` was not found in this workspace's organization.", ref)
	}

	return incident, nil
}

// activeChannelIncidents lists the still-open incidents whose Slack thread
// lives in this channel, newest first.
//
// Reads the reverse thread→incident state entries the notification sender
// writes when it posts an incident's top-level message, which is the only
// mapping that says "this incident is being discussed HERE".
func (h *Handler) activeChannelIncidents(
	ctx context.Context, orgUID, teamID, channelID string,
) []*models.Incident {
	prefix := reverseThreadStatePrefix + teamID + "/" + channelID + "/"

	entries, err := h.svc.db.ListStateEntries(ctx, nil, prefix)
	if err != nil {
		slog.WarnContext(ctx, "Failed to list Slack thread mappings for /comment",
			"team_id", teamID, "channel_id", channelID, "error", err)

		return nil
	}

	seen := make(map[string]bool, len(entries))
	out := make([]*models.Incident, 0, len(entries))

	for _, entry := range entries {
		if entry.Value == nil {
			continue
		}

		incidentUID, _ := (*entry.Value)[ThreadIncidentUIDKey].(string)
		entryOrgUID, _ := (*entry.Value)[ThreadOrgUIDKey].(string)

		if incidentUID == "" || entryOrgUID != orgUID || seen[incidentUID] {
			continue
		}

		seen[incidentUID] = true

		incident, incErr := h.svc.db.GetIncident(ctx, orgUID, incidentUID)
		if incErr != nil || incident == nil {
			continue
		}

		// Resolved incidents are history: a comment on one is almost always a
		// misfire, and including them would make the "exactly one" rule fail
		// in every channel that has ever seen an outage.
		if incident.State == models.IncidentStateResolved {
			continue
		}

		out = append(out, incident)
	}

	return out
}

// candidateLines renders the disambiguation list, capped.
func candidateLines(incidents []*models.Incident) []string {
	limit := len(incidents)
	if limit > maxCommentCandidates {
		limit = maxCommentCandidates
	}

	lines := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		lines = append(lines, "• "+incidentLabel(incidents[i]))
	}

	if len(incidents) > limit {
		lines = append(lines, fmt.Sprintf("• …and %d more", len(incidents)-limit))
	}

	return lines
}

// incidentLabel renders "#42 (api is down)", degrading gracefully for an
// incident created before the per-org numbers existed.
func incidentLabel(incident *models.Incident) string {
	title := ""
	if incident.Title != nil {
		title = strings.TrimSpace(*incident.Title)
	}

	ref := "the incident"
	if incident.Number > 0 {
		ref = fmt.Sprintf("#%d", incident.Number)
	}

	if title == "" {
		return ref
	}

	return ref + " (" + title + ")"
}

// ephemeral wraps text in a reply only the invoking user sees.
func ephemeral(text string) *MessageResponse {
	return &MessageResponse{ResponseType: ResponseTypeEphemeral, Text: text}
}
