package slack

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

const (
	statusDown = "down"
	statusUp   = "up"
)

// Slack mention command names.
const (
	cmdChecks    = "checks"
	cmdIncidents = "incidents"
	cmdConfig    = "config"
	cmdHelp      = "help"

	subAdd  = "add"
	subList = "list"
)

// handleMentionCommand routes a parsed command to the appropriate handler.
func (h *Handler) handleMentionCommand(ctx context.Context, event *Event, cmd *ParsedCommand) error {
	switch cmd.Command {
	case cmdChecks:
		return h.handleChecksCommand(ctx, event, cmd)
	case "results":
		return h.handleResultsCommand(ctx, event, cmd)
	case cmdIncidents:
		return h.handleIncidentsCommand(ctx, event, cmd)
	case cmdConfig:
		return h.handleConfigCommand(ctx, event, cmd)
	case cmdHelp, "":
		return h.handleHelpCommand(ctx, event)
	default:
		errMsg := fmt.Sprintf("Unknown command: `%s`. Type `@solidping help` for available commands.", cmd.Command)
		return h.sendMentionError(ctx, event, errMsg)
	}
}

// handleChecksCommand handles the checks subcommands.
func (h *Handler) handleChecksCommand(ctx context.Context, event *Event, cmd *ParsedCommand) error {
	switch cmd.Subcommand {
	case subAdd:
		return h.handleChecksAdd(ctx, event, cmd)
	case subList, "ls", "":
		return h.handleChecksList(ctx, event)
	case "rm", "remove", "delete":
		return h.handleChecksRemove(ctx, event, cmd)
	default:
		errMsg := fmt.Sprintf("Unknown checks subcommand: `%s`. Available: `add`, `list`, `rm`", cmd.Subcommand)
		return h.sendMentionError(ctx, event, errMsg)
	}
}

// handleChecksAdd handles the "checks add" command.
func (h *Handler) handleChecksAdd(ctx context.Context, event *Event, cmd *ParsedCommand) error {
	if len(cmd.Args) == 0 {
		return h.sendMentionError(ctx, event,
			"Missing URL. Usage: `@solidping checks add <url> [-slug name] [-interval 30s]`")
	}

	url := cmd.Args[0]
	slug := ""
	period := ""

	// Handle special case for "fake" - creates a test check with specific configuration
	if url == "fake" {
		url = "http://localhost:4000/api/v1/fake?period=120"
		slug = "fake"
		period = "PT10S" // ISO8601 format for 10 seconds
	} else if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		// Validate URL has protocol
		url = "https://" + url
	}

	// Create the check using existing service method
	result, err := h.svc.CreateCheckWithOptions(ctx, event.TeamID, url, slug, period)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to create check from mention",
			"url", url,
			"team_id", event.TeamID,
			"error", err,
		)
		return h.sendMentionError(ctx, event, "Failed to create check: "+err.Error())
	}

	// Build response message
	msg := &MessageResponse{
		Text: fmt.Sprintf("Check `%s` added", result.Slug),
		Blocks: []Block{
			{
				Type: BlockTypeSection,
				Text: &Text{
					Type: BlockTypeMrkdwn,
					Text: fmt.Sprintf(":white_check_mark: Check `%s` added for <%s|%s>", result.Slug, url, result.Name),
				},
			},
		},
	}

	return h.sendMentionResponse(ctx, event, msg)
}

// handleChecksList handles the "checks list" command.
func (h *Handler) handleChecksList(ctx context.Context, event *Event) error {
	// Get org from team ID
	conn, err := h.svc.GetConnectionByTeamID(ctx, event.TeamID)
	if err != nil {
		return h.sendMentionError(ctx, event, "Workspace not connected. Please reinstall the SolidPing app.")
	}

	org, err := h.svc.db.GetOrganization(ctx, conn.OrganizationUID)
	if err != nil {
		return h.sendMentionError(ctx, event, "Organization not found.")
	}

	// List checks
	checksResp, err := h.svc.checksService.ListChecks(ctx, org.Slug, checks.ListChecksOptions{
		IncludeLastStatusChange: true,
	})
	if err != nil {
		slog.ErrorContext(ctx, "Failed to list checks", "error", err)
		return h.sendMentionError(ctx, event, "Failed to list checks.")
	}

	if len(checksResp.Data) == 0 {
		return h.sendMentionResponse(ctx, event, &MessageResponse{
			Text: "No checks found. Use `@solidping checks add <url>` to create one.",
		})
	}

	// Build check list
	checksList := checksResp.Data
	lines := make([]string, 0, len(checksList))
	for i := range checksList {
		check := &checksList[i]
		slug := ""
		if check.Slug != nil {
			slug = *check.Slug
		}

		period := "1m"
		if check.Period != nil {
			period = *check.Period
		}

		status := ""
		if check.LastStatusChange != nil {
			dur := time.Since(check.LastStatusChange.Time)
			status = fmt.Sprintf("%s for %s", check.LastStatusChange.Status, timeutils.FormatHumanReadable(dur))
		}

		line := fmt.Sprintf("- `%s`, every %s", slug, timeutils.FormatPeriod(period))
		if status != "" {
			line += ", " + status
		}
		lines = append(lines, line)
	}

	msg := &MessageResponse{
		Text: fmt.Sprintf("%d checks", len(checksList)),
		Blocks: []Block{
			{
				Type: BlockTypeSection,
				Text: &Text{
					Type: BlockTypeMrkdwn,
					Text: strings.Join(lines, "\n"),
				},
			},
		},
	}

	return h.sendMentionResponse(ctx, event, msg)
}

// handleChecksRemove handles the "checks rm" command.
func (h *Handler) handleChecksRemove(ctx context.Context, event *Event, cmd *ParsedCommand) error {
	if len(cmd.Args) == 0 {
		return h.sendMentionError(ctx, event, "Missing check slug. Usage: `@solidping checks rm <slug>`")
	}

	checkSlug := cmd.Args[0]

	// Get org from team ID
	conn, err := h.svc.GetConnectionByTeamID(ctx, event.TeamID)
	if err != nil {
		return h.sendMentionError(ctx, event, "Workspace not connected.")
	}

	org, err := h.svc.db.GetOrganization(ctx, conn.OrganizationUID)
	if err != nil {
		return h.sendMentionError(ctx, event, "Organization not found.")
	}

	// Delete check
	if err := h.svc.checksService.DeleteCheck(ctx, org.Slug, checkSlug); err != nil {
		slog.ErrorContext(ctx, "Failed to delete check", "slug", checkSlug, "error", err)
		return h.sendMentionError(ctx, event, "Failed to remove check: "+err.Error())
	}

	msg := &MessageResponse{
		Text: fmt.Sprintf("Check `%s` removed", checkSlug),
		Blocks: []Block{
			{
				Type: BlockTypeSection,
				Text: &Text{
					Type: BlockTypeMrkdwn,
					Text: fmt.Sprintf(":wastebasket: Check `%s` removed", checkSlug),
				},
			},
		},
	}

	return h.sendMentionResponse(ctx, event, msg)
}

// handleResultsCommand handles the results command.
func (h *Handler) handleResultsCommand(ctx context.Context, event *Event, cmd *ParsedCommand) error {
	checkSlug := cmd.Flags["check"]
	if checkSlug == "" && len(cmd.Args) > 0 {
		checkSlug = cmd.Args[0]
	}

	if checkSlug == "" {
		return h.sendMentionError(ctx, event, "Missing check. Usage: `@solidping results -check <slug>`")
	}

	// Get org from team ID
	conn, err := h.svc.GetConnectionByTeamID(ctx, event.TeamID)
	if err != nil {
		return h.sendMentionError(ctx, event, "Workspace not connected.")
	}

	org, err := h.svc.db.GetOrganization(ctx, conn.OrganizationUID)
	if err != nil {
		return h.sendMentionError(ctx, event, "Organization not found.")
	}

	// Get the check by slug
	check, err := h.svc.db.GetCheckByUidOrSlug(ctx, org.UID, checkSlug)
	if err != nil || check == nil {
		return h.sendMentionError(ctx, event, fmt.Sprintf("Check `%s` not found.", checkSlug))
	}

	// List recent results directly from DB
	filter := &models.ListResultsFilter{
		OrganizationUID: org.UID,
		CheckUIDs:       []string{check.UID},
		Limit:           5,
	}

	dbResults, err := h.svc.db.ListResults(ctx, filter)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to list results", "check", checkSlug, "error", err)
		return h.sendMentionError(ctx, event, "Failed to list results.")
	}

	if len(dbResults.Results) == 0 {
		return h.sendMentionResponse(ctx, event, &MessageResponse{
			Text: fmt.Sprintf("No results found for check `%s`.", checkSlug),
		})
	}

	// Build results list
	lines := make([]string, 0, len(dbResults.Results))
	for _, result := range dbResults.Results {
		status := statusIntToString(result.Status)
		duration := ""
		if result.Duration != nil {
			duration = fmt.Sprintf(", %.0f ms", *result.Duration)
		}

		statusEmoji := ":white_check_mark:"
		if status == statusDown {
			statusEmoji = ":x:"
		}

		line := fmt.Sprintf("- `%s` %s %s%s", checkSlug, statusEmoji, status, duration)
		lines = append(lines, line)
	}

	msg := &MessageResponse{
		Text: "Results for " + checkSlug,
		Blocks: []Block{
			{
				Type: BlockTypeSection,
				Text: &Text{
					Type: BlockTypeMrkdwn,
					Text: strings.Join(lines, "\n"),
				},
			},
		},
	}

	return h.sendMentionResponse(ctx, event, msg)
}

// handleIncidentsCommand handles the incidents command.
//
//nolint:cyclop,funlen // Complex due to multiple validations and data fetching.
func (h *Handler) handleIncidentsCommand(ctx context.Context, event *Event, cmd *ParsedCommand) error {
	// Default to "list" if no subcommand
	if cmd.Subcommand != "" && cmd.Subcommand != subList && cmd.Subcommand != "ls" {
		errMsg := fmt.Sprintf("Unknown incidents subcommand: `%s`. Available: `list`", cmd.Subcommand)
		return h.sendMentionError(ctx, event, errMsg)
	}

	// Get org from team ID
	conn, err := h.svc.GetConnectionByTeamID(ctx, event.TeamID)
	if err != nil {
		return h.sendMentionError(ctx, event, "Workspace not connected.")
	}

	org, err := h.svc.db.GetOrganization(ctx, conn.OrganizationUID)
	if err != nil {
		return h.sendMentionError(ctx, event, "Organization not found.")
	}

	// Build filter
	filter := &models.ListIncidentsFilter{
		OrganizationUID: org.UID,
		Limit:           10,
	}

	// Filter by check if specified
	checkSlug := cmd.Flags["check"]
	if checkSlug == "" && len(cmd.Args) > 0 {
		checkSlug = cmd.Args[0]
	}

	if checkSlug != "" {
		// Resolve check slug to UID
		check, checkErr := h.svc.db.GetCheckByUidOrSlug(ctx, org.UID, checkSlug)
		if checkErr != nil || check == nil {
			return h.sendMentionError(ctx, event, fmt.Sprintf("Check `%s` not found.", checkSlug))
		}
		filter.CheckUIDs = []string{check.UID}
	}

	// List incidents directly from DB
	incidentsList, err := h.svc.db.ListIncidents(ctx, filter)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to list incidents", "error", err)
		return h.sendMentionError(ctx, event, "Failed to list incidents.")
	}

	if len(incidentsList) == 0 {
		text := "No incidents found."
		if checkSlug != "" {
			text = fmt.Sprintf("No incidents found for check `%s`.", checkSlug)
		}
		return h.sendMentionResponse(ctx, event, &MessageResponse{Text: text})
	}

	// Fetch check info for incidents
	checkMap := make(map[string]*models.Check)
	for _, inc := range incidentsList {
		if _, exists := checkMap[inc.CheckUID]; !exists {
			check, checkErr := h.svc.db.GetCheck(ctx, org.UID, inc.CheckUID)
			if checkErr == nil {
				checkMap[inc.CheckUID] = check
			}
		}
	}

	// Build incidents list
	lines := make([]string, 0, len(incidentsList))
	for _, incident := range incidentsList {
		checkInfo := ""
		if check, exists := checkMap[incident.CheckUID]; exists && check.Slug != nil {
			checkInfo = *check.Slug
		}

		stateEmoji := ":red_circle:"
		stateText := statusDown
		durationText := ""

		if incident.State == models.IncidentStateResolved {
			stateEmoji = ":large_green_circle:"
			stateText = "OK"
			if incident.ResolvedAt != nil {
				ago := time.Since(*incident.ResolvedAt)
				dur := incident.ResolvedAt.Sub(incident.StartedAt)
				durationText = fmt.Sprintf(" since %s ago, duration: %s",
					timeutils.FormatHumanReadable(ago), timeutils.FormatHumanReadable(dur))
			}
		} else {
			ago := time.Since(incident.StartedAt)
			durationText = " for " + timeutils.FormatHumanReadable(ago)
		}

		line := fmt.Sprintf("- `%s` %s %s%s", checkInfo, stateEmoji, stateText, durationText)
		lines = append(lines, line)
	}

	msg := &MessageResponse{
		Text: fmt.Sprintf("%d incidents", len(incidentsList)),
		Blocks: []Block{
			{
				Type: BlockTypeSection,
				Text: &Text{
					Type: BlockTypeMrkdwn,
					Text: strings.Join(lines, "\n"),
				},
			},
		},
	}

	return h.sendMentionResponse(ctx, event, msg)
}

// statusIntToString converts a result status int to a string.
func statusIntToString(status *int) string {
	if status == nil {
		return "unknown"
	}

	switch *status {
	case int(models.ResultStatusUp):
		return statusUp
	case int(models.ResultStatusDown), int(models.ResultStatusTimeout), int(models.ResultStatusError):
		return statusDown
	default:
		return "unknown"
	}
}

// handleConfigCommand handles the config subcommands.
func (h *Handler) handleConfigCommand(ctx context.Context, event *Event, cmd *ParsedCommand) error {
	switch cmd.Subcommand {
	case "default-channel":
		return h.handleConfigDefaultChannel(ctx, event, cmd)
	case "":
		return h.sendMentionError(ctx, event,
			"Missing config option. Usage: `@solidping config default-channel [#channel]`")
	default:
		errMsg := fmt.Sprintf("Unknown config option: `%s`. Available: `default-channel`", cmd.Subcommand)
		return h.sendMentionError(ctx, event, errMsg)
	}
}

// handleConfigDefaultChannel handles the "config default-channel" command.
func (h *Handler) handleConfigDefaultChannel(ctx context.Context, event *Event, cmd *ParsedCommand) error {
	var channelID string

	if len(cmd.Args) == 0 {
		// No channel specified, use the current channel
		channelID = event.Event.Channel
	} else {
		channelRef := cmd.Args[0]

		// Parse channel reference (could be #channel-name or <#C12345|channel-name>)
		channelID = parseChannelReference(channelRef)
		if channelID == "" {
			return h.sendMentionError(ctx, event,
				"Invalid channel format. Please mention the channel like <#channel>.")
		}
	}

	// Set the default channel without welcome message (user explicitly set it)
	if err := h.svc.SetDefaultChannel(ctx, event.TeamID, channelID, false); err != nil {
		slog.ErrorContext(ctx, "Failed to set default channel",
			"channel_id", channelID,
			"error", err,
		)
		return h.sendMentionError(ctx, event, "Failed to set default channel: "+err.Error())
	}

	msg := &MessageResponse{
		Text: "Default channel updated",
		Blocks: []Block{
			{
				Type: BlockTypeSection,
				Text: &Text{
					Type: BlockTypeMrkdwn,
					Text: fmt.Sprintf(":white_check_mark: Default notification channel set to <#%s>", channelID),
				},
			},
		},
	}

	return h.sendMentionResponse(ctx, event, msg)
}

// parseChannelReference extracts channel ID from various formats.
// Handles: <#C12345|channel-name>, <#C12345>, C12345.
func parseChannelReference(ref string) string {
	// Handle Slack's formatted channel reference: <#C12345|channel-name> or <#C12345>
	if strings.HasPrefix(ref, "<#") {
		ref = strings.TrimPrefix(ref, "<#")
		ref = strings.TrimSuffix(ref, ">")
		// Handle <#C12345|channel-name>
		if idx := strings.Index(ref, "|"); idx != -1 {
			return ref[:idx]
		}
		return ref
	}

	// Handle raw channel ID (starts with C)
	if strings.HasPrefix(ref, "C") && len(ref) > 1 {
		return ref
	}

	return ""
}

// handleHelpCommand sends help information.
func (h *Handler) handleHelpCommand(ctx context.Context, event *Event) error {
	helpText := "*Available Commands:*\n\n" +
		"*Checks*\n" +
		"- `checks add <url>` - Add a new check\n" +
		"- `checks list` - List all checks\n" +
		"- `checks rm <slug>` - Remove a check\n\n" +
		"*Results*\n" +
		"- `results -check <slug>` - Show recent results for a check\n\n" +
		"*Incidents*\n" +
		"- `incidents list` - List recent incidents\n" +
		"- `incidents list -check <slug>` - List incidents for a specific check\n\n" +
		"*Configuration*\n" +
		"- `config default-channel [#channel]` - Set default notification channel (uses current channel if omitted)\n\n" +
		"*Examples:*\n" +
		"`@solidping checks add https://example.com`\n" +
		"`@solidping checks list`\n" +
		"`@solidping results -check my-check`\n" +
		"`@solidping config default-channel` - Use current channel\n" +
		"`@solidping config default-channel #alerts` - Use specific channel"

	msg := &MessageResponse{
		Text: "SolidPing Help",
		Blocks: []Block{
			{
				Type: BlockTypeSection,
				Text: &Text{
					Type: BlockTypeMrkdwn,
					Text: helpText,
				},
			},
		},
	}

	return h.sendMentionResponse(ctx, event, msg)
}

// sendMentionResponse sends a response to a mention, in a thread.
func (h *Handler) sendMentionResponse(ctx context.Context, event *Event, msg *MessageResponse) error {
	client, err := h.svc.GetClient(ctx, event.TeamID)
	if err != nil {
		return fmt.Errorf("failed to get client: %w", err)
	}

	// Determine thread_ts: reply in existing thread or start new one
	threadTS := event.Event.ThreadTs
	if threadTS == "" {
		threadTS = event.Event.Ts // Start new thread from the mention message
	}

	if _, err := client.PostMessage(ctx, PostMessageOptions{
		Channel:  event.Event.Channel,
		ThreadTS: threadTS,
		Message:  msg,
	}); err != nil {
		return fmt.Errorf("failed to post message: %w", err)
	}

	return nil
}

// sendMentionError sends an error message in response to a mention.
func (h *Handler) sendMentionError(ctx context.Context, event *Event, errMsg string) error {
	msg := &MessageResponse{
		Text: errMsg,
		Blocks: []Block{
			{
				Type: BlockTypeSection,
				Text: &Text{
					Type: BlockTypeMrkdwn,
					Text: ":warning: " + errMsg,
				},
			},
		},
	}

	return h.sendMentionResponse(ctx, event, msg)
}
