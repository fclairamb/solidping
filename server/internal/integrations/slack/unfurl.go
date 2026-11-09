package slack

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// unfurlUptimeWindow is the trailing window of the uptime shown in a check
// unfurl.
const unfurlUptimeWindow = 24 * time.Hour

// unfurlEmojiNeutral marks a check whose state says nothing about its health
// (no result yet, no recent data, unknown status).
const unfurlEmojiNeutral = ":white_circle:"

// buildLinkUnfurl builds an unfurl preview for a SolidPing link.
//
// A check or incident link of the organization connected to the workspace
// renders as a short card: the entity's current state on the first line, the
// details (type, period, uptime, last result, or the incident's timing) in a
// context line. Links to another organization, or that don't resolve, keep the
// generic preview: the lookup is scoped to the workspace's own organization so
// an unfurl never leaks another tenant's data. Slack calls this through the
// authenticated app, so nothing here is served to anonymous crawlers.
func (h *Handler) buildLinkUnfurl(ctx context.Context, teamID, linkURL string) *Unfurl {
	return h.buildLinkUnfurlAt(ctx, teamID, linkURL, time.Now())
}

func (h *Handler) buildLinkUnfurlAt(ctx context.Context, teamID, linkURL string, now time.Time) *Unfurl {
	if blocks := h.resolveLinkUnfurl(ctx, teamID, linkURL, now); len(blocks) > 0 {
		return &Unfurl{Blocks: blocks}
	}

	return &Unfurl{
		Blocks: []Block{
			unfurlSection(fmt.Sprintf("*SolidPing Link*\n<%s|View in SolidPing>", linkURL)),
		},
	}
}

// resolveLinkUnfurl returns the card of a check or incident dashboard link, or
// nil when the link isn't one or doesn't resolve within the workspace's
// organization.
func (h *Handler) resolveLinkUnfurl(ctx context.Context, teamID, linkURL string, now time.Time) []Block {
	link, ok := parseDashboardEntityLink(linkURL)
	if !ok || h.svc == nil {
		return nil
	}

	conn, err := h.svc.GetConnectionByTeamID(ctx, teamID)
	if err != nil || conn == nil {
		return nil
	}

	org, err := h.svc.db.GetOrganization(ctx, conn.OrganizationUID)
	if err != nil || org == nil || !strings.EqualFold(org.Slug, link.orgSlug) {
		return nil
	}

	if link.kind == linkKindCheck {
		check, checkErr := h.svc.db.GetCheckByUidOrSlug(ctx, org.UID, link.id)
		if checkErr != nil || check == nil {
			return nil
		}

		return h.checkUnfurlBlocks(ctx, org.UID, check, linkURL, now)
	}

	incident, err := h.svc.db.GetIncident(ctx, org.UID, link.id)
	if err != nil || incident == nil {
		return nil
	}

	return []Block{
		unfurlSection(fmt.Sprintf("%s <%s|%s>", incidentUnfurlEmoji(incident), linkURL,
			slackEscape(h.unfurlIncidentLabel(ctx, org.UID, incident)))),
		unfurlContext(incidentUnfurlDetails(incident, now)),
	}
}

// checkUnfurlBlocks renders a check as "<state emoji> <name> · <state> for 3h"
// plus a context line with its type, period, 24h uptime and last result.
func (h *Handler) checkUnfurlBlocks(
	ctx context.Context, orgUID string, check *models.Check, linkURL string, now time.Time,
) []Block {
	emoji, state := checkUnfurlState(check, now)

	headline := fmt.Sprintf("%s <%s|%s> · *%s*", emoji, linkURL, slackEscape(unfurlCheckName(check)), state)
	if check.Enabled && check.StatusChangedAt != nil && !check.IsDataStale(now) {
		headline += " for " + timeutils.FormatHumanReadable(now.Sub(*check.StatusChangedAt))
	}

	details := make([]string, 0, 4)
	if check.Type != "" {
		details = append(details, strings.ToUpper(check.Type))
	}

	if period := time.Duration(check.Period); period > 0 {
		details = append(details, "every "+timeutils.FormatHumanReadable(period))
	}

	if pct, ok := h.checkUptime(ctx, orgUID, check.UID, now); ok {
		details = append(details, formatUnfurlUptime(pct)+" uptime (24h)")
	}

	if check.LastResultAt != nil {
		details = append(details, "last checked "+unfurlAgo(now.Sub(*check.LastResultAt)))
	}

	blocks := []Block{unfurlSection(headline)}
	if len(details) > 0 {
		blocks = append(blocks, unfurlContext(strings.Join(details, " · ")))
	}

	return blocks
}

// checkUptime returns the availability of a check over the trailing 24h from
// its raw results, with the shared availability rule (models.RawAvailability)
// the badges and status pages use. ok is false when there is nothing to count.
func (h *Handler) checkUptime(ctx context.Context, orgUID, checkUID string, now time.Time) (float64, bool) {
	start := now.Add(-unfurlUptimeWindow)

	res, err := h.svc.db.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID:  orgUID,
		CheckUIDs:        []string{checkUID},
		PeriodTypes:      []string{"raw"},
		PeriodStartAfter: &start,
		SkipBlobs:        true,
	})
	if err != nil || res == nil {
		return 0, false
	}

	success, total := models.RawAvailability(res.Results)
	if total == 0 {
		return 0, false
	}

	return float64(success) / float64(total) * 100, true
}

// checkUnfurlState maps a check to its emoji and human state.
func checkUnfurlState(check *models.Check, now time.Time) (string, string) {
	switch {
	case !check.Enabled:
		return ":double_vertical_bar:", "Paused"
	case check.IsDataStale(now):
		return unfurlEmojiNeutral, "No recent data"
	}

	switch check.Status {
	case models.CheckStatusUp:
		return ":large_green_circle:", "Up"
	case models.CheckStatusDown:
		return ":red_circle:", "Down"
	case models.CheckStatusDegraded:
		return ":large_yellow_circle:", "Degraded"
	case models.CheckStatusWarning:
		return ":large_yellow_circle:", "Warning"
	case models.CheckStatusValidating:
		return ":large_orange_circle:", "Confirming failure"
	case models.CheckStatusStale:
		return unfurlEmojiNeutral, "No recent data"
	case models.CheckStatusCreated:
		return unfurlEmojiNeutral, "Waiting for first result"
	default:
		return unfurlEmojiNeutral, "Unknown"
	}
}

func incidentUnfurlEmoji(incident *models.Incident) string {
	if incident.State == models.IncidentStateResolved {
		return ":white_check_mark:"
	}

	return ":rotating_light:"
}

// incidentUnfurlDetails is "Ongoing for 12m · acknowledged" or
// "Resolved 2h ago · lasted 14m".
func incidentUnfurlDetails(incident *models.Incident, now time.Time) string {
	if incident.State == models.IncidentStateResolved && incident.ResolvedAt != nil {
		return fmt.Sprintf("Resolved %s ago · lasted %s",
			timeutils.FormatHumanReadable(now.Sub(*incident.ResolvedAt)),
			timeutils.FormatHumanReadable(incident.ResolvedAt.Sub(incident.StartedAt)))
	}

	text := "Ongoing for " + timeutils.FormatHumanReadable(now.Sub(incident.StartedAt))
	if incident.AcknowledgedAt != nil {
		text += " · acknowledged"
	}

	return text
}

// formatUnfurlUptime truncates (never rounds up) to two decimals, so a check
// that missed one result in a day never reads "100.00%".
func formatUnfurlUptime(pct float64) string {
	if pct >= 100 {
		return "100%"
	}

	return fmt.Sprintf("%.2f%%", math.Floor(pct*100)/100)
}

// unfurlAgo is "just now" under a minute, else "3m ago".
func unfurlAgo(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}

	return timeutils.FormatHumanReadable(d) + " ago"
}

func unfurlSection(text string) Block {
	return Block{Type: BlockTypeSection, Text: &Text{Type: BlockTypeMrkdwn, Text: text}}
}

func unfurlContext(text string) Block {
	return Block{Type: BlockTypeContext, Elements: []any{ContextElement{Type: BlockTypeMrkdwn, Text: text}}}
}

// unfurlIncidentLabel renders an incident as "#42 · <title>", the title
// falling back to the check's name, as in SolidPing's own Slack messages.
func (h *Handler) unfurlIncidentLabel(ctx context.Context, orgUID string, incident *models.Incident) string {
	label := ""
	if incident.Title != nil && *incident.Title != "" {
		label = *incident.Title
	} else if check, err := h.svc.db.GetCheckByUidOrSlug(ctx, orgUID, incident.CheckUID); err == nil && check != nil {
		label = unfurlCheckName(check)
	}

	switch {
	case incident.Number > 0 && label != "":
		return fmt.Sprintf("#%d · %s", incident.Number, label)
	case incident.Number > 0:
		return fmt.Sprintf("#%d", incident.Number)
	case label != "":
		return label
	default:
		return "Incident"
	}
}
