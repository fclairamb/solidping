package msteams

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

const (
	statusDown    = "down"
	statusUp      = "up"
	statusWarning = "warning"
)

// Command names — deliberately identical to the Slack grammar
// (slack/mention_commands.go) so the two bots behave the same.
const (
	cmdChecks    = "checks"
	cmdResults   = "results"
	cmdIncidents = "incidents"
	cmdConfig    = "config"
	cmdHelp      = "help"
	cmdLink      = "link"

	subAdd            = "add"
	subList           = "list"
	subDefaultChannel = "default-channel"
)

// mentionPrefix is what the help text tells users to type. The parser strips
// the mention entity rather than this literal string, so an operator who
// renames the app in their own manifest still gets working commands — this
// constant only affects the wording of the help card.
const mentionPrefix = "@SolidPing"

// handleMentionCommand routes a parsed command to its handler.
func (h *Handler) handleMentionCommand(ctx context.Context, activity *Activity, cmd *ParsedCommand) error {
	switch cmd.Command {
	case cmdChecks:
		return h.handleChecksCommand(ctx, activity, cmd)
	case cmdResults:
		return h.handleResultsCommand(ctx, activity, cmd)
	case cmdIncidents:
		return h.handleIncidentsCommand(ctx, activity, cmd)
	case cmdConfig:
		return h.handleConfigCommand(ctx, activity, cmd)
	case cmdLink:
		return h.handleLinkCommand(ctx, activity, cmd)
	case cmdHelp, "":
		return h.handleHelpCommand(ctx, activity)
	default:
		return h.replyError(ctx, activity, fmt.Sprintf(
			"Unknown command: `%s`. Type `%s help` for available commands.", cmd.Command, mentionPrefix))
	}
}

// handleChecksCommand handles the `checks` subcommands.
func (h *Handler) handleChecksCommand(ctx context.Context, activity *Activity, cmd *ParsedCommand) error {
	switch cmd.Subcommand {
	case subAdd:
		return h.handleChecksAdd(ctx, activity, cmd)
	case subList, "ls", "":
		return h.handleChecksList(ctx, activity)
	case "rm", "remove", "delete":
		return h.handleChecksRemove(ctx, activity, cmd)
	default:
		return h.replyError(ctx, activity, fmt.Sprintf(
			"Unknown checks subcommand: `%s`. Available: `add`, `list`, `rm`", cmd.Subcommand))
	}
}

// handleChecksAdd handles `checks add <url>`.
func (h *Handler) handleChecksAdd(ctx context.Context, activity *Activity, cmd *ParsedCommand) error {
	if len(cmd.Args) == 0 {
		return h.replyError(ctx, activity,
			"Missing URL. Usage: `"+mentionPrefix+" checks add <url> [-slug name] [-interval 30s]`")
	}

	target := cmd.Args[0]
	slug := cmd.Flags["slug"]
	period := ""

	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = "https://" + target
	}

	result, err := h.svc.CreateCheckWithOptions(ctx, activity.TenantID(), target, slug, period)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to create check from Teams mention",
			"url", target, "tenant_id", activity.TenantID(), "error", err)

		return h.replyError(ctx, activity, "Failed to create check: "+err.Error())
	}

	return h.replyCard(ctx, activity,
		fmt.Sprintf("Check `%s` added", result.Slug),
		SimpleCard(
			fmt.Sprintf("✅ Check `%s` added", result.Slug),
			fmt.Sprintf("%s — %s", result.Name, target),
			CardColorGood,
		))
}

// handleChecksList handles `checks list`.
func (h *Handler) handleChecksList(ctx context.Context, activity *Activity) error {
	org, err := h.svc.orgForTenant(ctx, activity.TenantID())
	if err != nil {
		return h.replyTenantError(ctx, activity, err)
	}

	checksResp, err := h.svc.checksService.ListChecks(ctx, org.Slug, checks.ListChecksOptions{
		IncludeLastStatusChange: true,
	})
	if err != nil {
		slog.ErrorContext(ctx, "Failed to list checks for Teams command", "error", err)

		return h.replyError(ctx, activity, "Failed to list checks.")
	}

	if len(checksResp.Data) == 0 {
		return h.replyText(ctx, activity,
			"No checks found. Use `"+mentionPrefix+" checks add <url>` to create one.")
	}

	list := checksResp.Data
	lines := make([]string, 0, len(list))

	for i := range list {
		check := &list[i]

		slug := ""
		if check.Slug != nil {
			slug = *check.Slug
		}

		period := "1m"
		if check.Period != nil {
			period = *check.Period
		}

		line := fmt.Sprintf("- `%s`, every %s", slug, timeutils.FormatPeriod(period))

		if check.LastStatusChange != nil {
			line += fmt.Sprintf(", %s for %s",
				check.LastStatusChange.Status,
				timeutils.FormatHumanReadable(time.Since(check.LastStatusChange.Time)))
		}

		lines = append(lines, line)
	}

	return h.replyCard(ctx, activity,
		fmt.Sprintf("%d checks", len(list)),
		SimpleCard(fmt.Sprintf("%d checks", len(list)), strings.Join(lines, "\n\n"), ""))
}

// handleChecksRemove handles `checks rm <slug>`.
func (h *Handler) handleChecksRemove(ctx context.Context, activity *Activity, cmd *ParsedCommand) error {
	if len(cmd.Args) == 0 {
		return h.replyError(ctx, activity, "Missing check slug. Usage: `"+mentionPrefix+" checks rm <slug>`")
	}

	checkSlug := cmd.Args[0]

	org, err := h.svc.orgForTenant(ctx, activity.TenantID())
	if err != nil {
		return h.replyTenantError(ctx, activity, err)
	}

	if err := h.svc.checksService.DeleteCheck(ctx, org.Slug, checkSlug); err != nil {
		slog.ErrorContext(ctx, "Failed to delete check from Teams command", "slug", checkSlug, "error", err)

		return h.replyError(ctx, activity, "Failed to remove check: "+err.Error())
	}

	return h.replyCard(ctx, activity,
		fmt.Sprintf("Check `%s` removed", checkSlug),
		SimpleCard(fmt.Sprintf("🗑 Check `%s` removed", checkSlug), "", ""))
}

// handleResultsCommand handles `results -check <slug>`.
func (h *Handler) handleResultsCommand(ctx context.Context, activity *Activity, cmd *ParsedCommand) error {
	checkSlug := cmd.Flags["check"]
	if checkSlug == "" && len(cmd.Args) > 0 {
		checkSlug = cmd.Args[0]
	}

	if checkSlug == "" {
		return h.replyError(ctx, activity, "Missing check. Usage: `"+mentionPrefix+" results -check <slug>`")
	}

	org, err := h.svc.orgForTenant(ctx, activity.TenantID())
	if err != nil {
		return h.replyTenantError(ctx, activity, err)
	}

	check, err := h.svc.db.GetCheckByUidOrSlug(ctx, org.UID, checkSlug)
	if err != nil || check == nil {
		return h.replyError(ctx, activity, fmt.Sprintf("Check `%s` not found.", checkSlug))
	}

	dbResults, err := h.svc.db.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{check.UID},
		Limit:           5,
	})
	if err != nil {
		slog.ErrorContext(ctx, "Failed to list results for Teams command", "check", checkSlug, "error", err)

		return h.replyError(ctx, activity, "Failed to list results.")
	}

	if len(dbResults.Results) == 0 {
		return h.replyText(ctx, activity, fmt.Sprintf("No results found for check `%s`.", checkSlug))
	}

	lines := make([]string, 0, len(dbResults.Results))

	for _, result := range dbResults.Results {
		status := statusIntToString(result.Status)

		duration := ""
		if result.Duration != nil {
			duration = fmt.Sprintf(", %.0f ms", *result.Duration)
		}

		emoji := "✅"
		if status == statusDown {
			emoji = "❌"
		}

		lines = append(lines, fmt.Sprintf("- `%s` %s %s%s", checkSlug, emoji, status, duration))
	}

	return h.replyCard(ctx, activity,
		"Results for "+checkSlug,
		SimpleCard("Results for `"+checkSlug+"`", strings.Join(lines, "\n\n"), ""))
}

// handleIncidentsCommand handles `incidents list [-check <slug>]`.
func (h *Handler) handleIncidentsCommand(ctx context.Context, activity *Activity, cmd *ParsedCommand) error {
	if cmd.Subcommand != "" && cmd.Subcommand != subList && cmd.Subcommand != "ls" {
		return h.replyError(ctx, activity, fmt.Sprintf(
			"Unknown incidents subcommand: `%s`. Available: `list`", cmd.Subcommand))
	}

	org, err := h.svc.orgForTenant(ctx, activity.TenantID())
	if err != nil {
		return h.replyTenantError(ctx, activity, err)
	}

	filter := &models.ListIncidentsFilter{OrganizationUID: org.UID, Limit: 10}

	checkSlug := cmd.Flags["check"]
	if checkSlug == "" && len(cmd.Args) > 0 {
		checkSlug = cmd.Args[0]
	}

	if checkSlug != "" {
		check, checkErr := h.svc.db.GetCheckByUidOrSlug(ctx, org.UID, checkSlug)
		if checkErr != nil || check == nil {
			return h.replyError(ctx, activity, fmt.Sprintf("Check `%s` not found.", checkSlug))
		}

		filter.CheckUIDs = []string{check.UID}
	}

	incidentsList, err := h.svc.db.ListIncidents(ctx, filter)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to list incidents for Teams command", "error", err)

		return h.replyError(ctx, activity, "Failed to list incidents.")
	}

	if len(incidentsList) == 0 {
		text := "No incidents found."
		if checkSlug != "" {
			text = fmt.Sprintf("No incidents found for check `%s`.", checkSlug)
		}

		return h.replyText(ctx, activity, text)
	}

	lines := h.buildIncidentLines(ctx, org.UID, incidentsList)

	return h.replyCard(ctx, activity,
		fmt.Sprintf("%d incidents", len(incidentsList)),
		SimpleCard(fmt.Sprintf("%d incidents", len(incidentsList)), strings.Join(lines, "\n\n"), ""))
}

// buildIncidentLines renders each incident as one markdown bullet, resolving
// the owning check's slug best-effort.
func (h *Handler) buildIncidentLines(
	ctx context.Context, orgUID string, incidentsList []*models.Incident,
) []string {
	checkMap := make(map[string]*models.Check, len(incidentsList))

	for _, inc := range incidentsList {
		if _, exists := checkMap[inc.CheckUID]; exists {
			continue
		}

		if check, err := h.svc.db.GetCheck(ctx, orgUID, inc.CheckUID); err == nil {
			checkMap[inc.CheckUID] = check
		}
	}

	lines := make([]string, 0, len(incidentsList))

	for _, incident := range incidentsList {
		checkInfo := ""
		if check, exists := checkMap[incident.CheckUID]; exists && check.Slug != nil {
			checkInfo = *check.Slug
		}

		emoji := "🔴"
		stateText := statusDown
		durationText := ""

		if incident.State == models.IncidentStateResolved {
			emoji = "🟢"
			stateText = "OK"

			if incident.ResolvedAt != nil {
				durationText = fmt.Sprintf(" since %s ago, duration: %s",
					timeutils.FormatHumanReadable(time.Since(*incident.ResolvedAt)),
					timeutils.FormatHumanReadable(incident.ResolvedAt.Sub(incident.StartedAt)))
			}
		} else {
			durationText = " for " + timeutils.FormatHumanReadable(time.Since(incident.StartedAt))
		}

		lines = append(lines, fmt.Sprintf("- `%s` %s %s%s", checkInfo, emoji, stateText, durationText))
	}

	return lines
}

// statusIntToString converts a result status int to a display string.
func statusIntToString(status *int) string {
	if status == nil {
		return "unknown"
	}

	switch *status {
	case int(models.ResultStatusUp):
		return statusUp
	case int(models.ResultStatusWarning):
		return statusWarning
	case int(models.ResultStatusDown), int(models.ResultStatusTimeout), int(models.ResultStatusError):
		return statusDown
	default:
		return "unknown"
	}
}

// handleConfigCommand handles the `config` subcommands.
func (h *Handler) handleConfigCommand(ctx context.Context, activity *Activity, cmd *ParsedCommand) error {
	switch cmd.Subcommand {
	case subDefaultChannel:
		return h.handleConfigDefaultChannel(ctx, activity, cmd)
	case "":
		return h.replyError(ctx, activity,
			"Missing config option. Usage: `"+mentionPrefix+" config default-channel`")
	default:
		return h.replyError(ctx, activity, fmt.Sprintf(
			"Unknown config option: `%s`. Available: `default-channel`", cmd.Subcommand))
	}
}

// handleConfigDefaultChannel implements `config default-channel`.
//
// Teams channels are per-team and there is no cross-team channel-reference
// syntax (Slack's `<#C123|name>` has no equivalent), so the command is scoped
// to the team the message came from: with no argument it makes the current
// conversation the default. An explicit argument must be a conversation id
// the bot has already been added to — anything else is rejected with a
// pointer at the dashboard picker rather than silently doing nothing.
func (h *Handler) handleConfigDefaultChannel(
	ctx context.Context, activity *Activity, cmd *ParsedCommand,
) error {
	destID := activity.ConversationID()
	if len(cmd.Args) > 0 {
		destID = cmd.Args[0]
	}

	if destID == "" {
		return h.replyError(ctx, activity, "Could not determine the current channel.")
	}

	// Make sure the current conversation is a known destination before trying
	// to select it: a channel the bot was added to before this feature
	// existed may not be captured yet.
	if err := h.svc.RegisterConversation(ctx, activity); err != nil && !errors.Is(err, ErrTenantNotLinked) {
		slog.WarnContext(ctx, "Failed to register Teams conversation before setting default",
			"tenant_id", activity.TenantID(), "error", err)
	}

	dest, err := h.svc.SetDefaultDestination(ctx, activity.TenantID(), destID)
	if err != nil {
		if errors.Is(err, ErrDestinationNotFound) {
			return h.replyError(ctx, activity,
				"That channel is not one this bot has been added to. Add SolidPing to the channel first, "+
					"or pick it in the dashboard under Integrations → Microsoft Teams.")
		}

		return h.replyTenantError(ctx, activity, err)
	}

	name := dest.Name
	if name == "" {
		name = "this channel"
	}

	return h.replyCard(ctx, activity,
		"Default channel updated",
		SimpleCard("✅ Default notification channel set to "+name, "", CardColorGood))
}

// handleHelpCommand sends the help card.
func (h *Handler) handleHelpCommand(ctx context.Context, activity *Activity) error {
	body := strings.Join([]string{
		"**Checks**",
		"- `checks add <url>` — add a new check",
		"- `checks list` — list all checks",
		"- `checks rm <slug>` — remove a check",
		"",
		"**Results**",
		"- `results -check <slug>` — show recent results for a check",
		"",
		"**Incidents**",
		"- `incidents list` — list recent incidents",
		"- `incidents list -check <slug>` — incidents for one check",
		"",
		"**Configuration**",
		"- `config default-channel` — send notifications to this channel",
		"- `link <code>` — connect this Microsoft 365 tenant to a SolidPing organization",
		"",
		"**Examples**",
		"`" + mentionPrefix + " checks add https://example.com`",
		"`" + mentionPrefix + " incidents list`",
	}, "\n\n")

	return h.replyCard(ctx, activity, "SolidPing help", SimpleCard("SolidPing", body, ""))
}

// handleLinkCommand redeems a linking code, binding this Microsoft 365
// tenant to the SolidPing organization that minted the code.
//
// This is the proof-of-possession step: the code was issued to an
// authenticated org admin in the dashboard, and it can only be redeemed from
// inside a real, signature-verified Bot Framework activity. Quoting it here
// is what demonstrates that the same actor controls both sides. Failures are
// deliberately indistinguishable ("invalid or expired") so the command cannot
// be used as an oracle for probing other orgs' codes.
func (h *Handler) handleLinkCommand(ctx context.Context, activity *Activity, cmd *ParsedCommand) error {
	if len(cmd.Args) == 0 {
		return h.replyError(ctx, activity,
			"Missing link code. Run `"+mentionPrefix+" link <code>` with the code from "+
				"**Integrations → Microsoft Teams (bot)** in your SolidPing dashboard.")
	}

	conn, err := h.svc.LinkTenant(ctx, cmd.Args[0], activity)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidLinkCode), errors.Is(err, ErrConnectionNotFound):
			return h.replyError(ctx, activity,
				"That link code is invalid or has expired. Generate a fresh one in your SolidPing dashboard.")
		case errors.Is(err, ErrTenantAlreadyLinked):
			return h.replyError(ctx, activity,
				"This Microsoft 365 tenant is already linked to a SolidPing organization. "+
					"Remove the existing Microsoft Teams integration there first.")
		case errors.Is(err, ErrTenantNotAllowed):
			return h.replyError(ctx, activity,
				"This SolidPing instance only accepts a different Microsoft 365 tenant.")
		default:
			slog.ErrorContext(ctx, "Microsoft Teams link failed", "error", err)

			return h.replyError(ctx, activity, "Could not complete the link. Please try again.")
		}
	}

	slog.InfoContext(ctx, "Microsoft Teams tenant linked via chat command",
		"connection_uid", conn.UID, "org_uid", conn.OrganizationUID)

	return h.replyCard(ctx, activity, "Connected", SimpleCard(
		"✅ SolidPing is connected",
		"This channel is now a notification destination. Type `"+mentionPrefix+" help` to see what I can do.",
		CardColorGood))
}

// replyTenantError renders the right message for a routing failure.
//
// The unlinked-tenant case points at the linking code rather than echoing the
// tenant GUID: a tenant id is not proof of ownership, so telling the channel
// to paste it into a dashboard was the wrong instruction — anyone could do
// that for any tenant.
func (h *Handler) replyTenantError(ctx context.Context, activity *Activity, err error) error {
	switch {
	case errors.Is(err, ErrTenantNotLinked):
		return h.replyCard(ctx, activity, "Not connected", SimpleCard(
			"SolidPing is not connected to this Microsoft 365 tenant",
			"In your SolidPing dashboard open **Integrations → Microsoft Teams (bot)**, click "+
				"**Connect Microsoft Teams** to get a link code, then run "+
				"`"+mentionPrefix+" link <code>` here.",
			CardColorWarning))
	case errors.Is(err, ErrUninstalled):
		return h.replyError(ctx, activity,
			"This SolidPing connection is marked uninstalled. Reinstall the app to resume.")
	case errors.Is(err, ErrTenantNotAllowed):
		return h.replyError(ctx, activity,
			"This SolidPing instance is configured for a different Microsoft 365 tenant.")
	case errors.Is(err, ErrAmbiguousTenant):
		return h.replyError(ctx, activity,
			"This Microsoft 365 tenant maps to more than one SolidPing integration. "+
				"Ask an administrator to remove the duplicate before using the bot.")
	default:
		slog.ErrorContext(ctx, "Microsoft Teams command failed", "tenant_id", activity.TenantID(), "error", err)

		return h.replyError(ctx, activity, "Something went wrong handling that command.")
	}
}
