package discord

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// Command names.
const (
	cmdChecks    = "checks"
	cmdIncidents = "incidents"
	cmdConfig    = "config"
	cmdComment   = "comment"
	cmdCheck     = "check"
	cmdHelp      = "help"

	subAdd  = "add"
	subList = "list"
)

// Command is a transport-agnostic SolidPing command invocation.
//
// Both entry points build one of these — the HTTP interactions endpoint from a
// Discord application command, and the Gateway from an `@SolidPing …` mention —
// so the command set is implemented exactly once and cannot drift between
// transports.
type Command struct {
	GuildID   string
	ChannelID string
	// ThreadID is set when the command was invoked inside a thread. It is what
	// lets `comment` figure out which incident is being discussed without the
	// user naming it.
	ThreadID string
	UserID   string
	UserName string

	Command    string
	Subcommand string
	Args       []string
	Flags      map[string]string
}

// CommandResponse is the reply a command produces.
type CommandResponse struct {
	Text string
	// Ephemeral marks a reply only the invoking user should see. Honored by
	// the interactions transport; the Gateway has no ephemeral messages, so it
	// posts the reply normally.
	Ephemeral bool
}

func reply(text string) *CommandResponse     { return &CommandResponse{Text: text} }
func ephemeral(text string) *CommandResponse { return &CommandResponse{Text: text, Ephemeral: true} }

// DispatchCommand is the single dispatch entry for SolidPing commands.
func DispatchCommand(ctx context.Context, svc *Service, cmd *Command) (*CommandResponse, error) {
	slog.InfoContext(ctx, "Handling Discord command",
		"command", cmd.Command, "subcommand", cmd.Subcommand,
		"guild_id", cmd.GuildID, "user_id", cmd.UserID)

	switch cmd.Command {
	case cmdChecks:
		return dispatchChecks(ctx, svc, cmd)
	case cmdCheck:
		return checksAdd(ctx, svc, cmd)
	case cmdIncidents:
		return incidentsList(ctx, svc, cmd)
	case cmdConfig:
		return dispatchConfig(ctx, svc, cmd)
	case cmdComment:
		return commentCommand(ctx, svc, cmd)
	case cmdHelp, "":
		return reply(helpText()), nil
	default:
		return ephemeral(fmt.Sprintf(
			"Unknown command: `%s`. Try `/solidping help`.", cmd.Command)), nil
	}
}

func dispatchChecks(ctx context.Context, svc *Service, cmd *Command) (*CommandResponse, error) {
	switch cmd.Subcommand {
	case subAdd:
		return checksAdd(ctx, svc, cmd)
	case subList, "ls", "":
		return checksList(ctx, svc, cmd)
	case "rm", "remove", "delete":
		return checksRemove(ctx, svc, cmd)
	default:
		return ephemeral(fmt.Sprintf(
			"Unknown checks subcommand: `%s`. Available: `add`, `list`, `rm`.", cmd.Subcommand)), nil
	}
}

func dispatchConfig(ctx context.Context, svc *Service, cmd *Command) (*CommandResponse, error) {
	if cmd.Subcommand == "default-channel" {
		return configDefaultChannel(ctx, svc, cmd)
	}

	return ephemeral("Usage: `/solidping config default-channel #channel`"), nil
}

// resolveOrg maps the invoking guild to its organization.
func resolveOrg(ctx context.Context, svc *Service, guildID string) (*models.Organization, string, error) {
	conn, err := svc.GetConnectionByGuildID(ctx, guildID)
	if err != nil || conn == nil {
		return nil, "", ErrConnectionNotFound
	}

	org, err := svc.db.GetOrganization(ctx, conn.OrganizationUID)
	if err != nil {
		return nil, "", ErrOrganizationNotFound
	}

	return org, conn.UID, nil
}

const notConnectedMessage = "This Discord server is not connected to SolidPing. " +
	"Install the SolidPing bot from your organization's dashboard."

// checksAdd implements `checks add <url>` and the `check <url>` shorthand.
func checksAdd(ctx context.Context, svc *Service, cmd *Command) (*CommandResponse, error) {
	if len(cmd.Args) == 0 {
		return ephemeral("Missing URL. Usage: `/solidping checks add <url>`"), nil
	}

	target := normalizeCheckURL(cmd.Args[0])
	if target == "" {
		return ephemeral("Invalid URL. Please provide a valid HTTP or HTTPS URL."), nil
	}

	result, err := svc.CreateCheck(ctx, cmd.GuildID, target)
	if err != nil {
		slog.ErrorContext(ctx, "Discord: failed to create check", "url", target, "error", err)

		return ephemeral("Failed to create check: " + err.Error()), nil
	}

	return reply(fmt.Sprintf("✅ Check `%s` created for %s", result.Slug, target)), nil
}

// normalizeCheckURL accepts a bare host and returns "" when the value cannot be
// a monitored HTTP target.
func normalizeCheckURL(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}

	if !strings.HasPrefix(text, "http://") && !strings.HasPrefix(text, "https://") {
		text = "https://" + text
	}

	parsed, err := url.Parse(text)
	if err != nil || parsed.Host == "" {
		return ""
	}

	return text
}

func checksList(ctx context.Context, svc *Service, cmd *Command) (*CommandResponse, error) {
	org, _, err := resolveOrg(ctx, svc, cmd.GuildID)
	if err != nil {
		return ephemeral(notConnectedMessage), nil
	}

	resp, err := svc.checksService.ListChecks(ctx, org.Slug, checks.ListChecksOptions{
		IncludeLastStatusChange: true,
	})
	if err != nil {
		slog.ErrorContext(ctx, "Discord: failed to list checks", "error", err)

		return ephemeral("Failed to list checks."), nil
	}

	if len(resp.Data) == 0 {
		return reply("No checks yet. Use `/solidping checks add <url>` to create one."), nil
	}

	lines := make([]string, 0, len(resp.Data))

	for i := range resp.Data {
		check := &resp.Data[i]

		slug := ""
		if check.Slug != nil {
			slug = *check.Slug
		}

		period := "1m"
		if check.Period != nil {
			period = *check.Period
		}

		line := fmt.Sprintf("• `%s`, every %s", slug, timeutils.FormatPeriod(period))

		if check.LastStatusChange != nil {
			line += fmt.Sprintf(", %s for %s", check.LastStatusChange.Status,
				timeutils.FormatHumanReadable(time.Since(check.LastStatusChange.Time)))
		}

		lines = append(lines, line)
	}

	return reply(fmt.Sprintf("**%d checks**\n%s", len(resp.Data), strings.Join(lines, "\n"))), nil
}

func checksRemove(ctx context.Context, svc *Service, cmd *Command) (*CommandResponse, error) {
	if len(cmd.Args) == 0 {
		return ephemeral("Missing check slug. Usage: `/solidping checks rm <slug>`"), nil
	}

	org, _, err := resolveOrg(ctx, svc, cmd.GuildID)
	if err != nil {
		return ephemeral(notConnectedMessage), nil
	}

	slug := cmd.Args[0]

	if err := svc.checksService.DeleteCheck(ctx, org.Slug, slug); err != nil {
		slog.ErrorContext(ctx, "Discord: failed to delete check", "slug", slug, "error", err)

		return ephemeral("Failed to remove check: " + err.Error()), nil
	}

	return reply(fmt.Sprintf("🗑️ Check `%s` removed", slug)), nil
}

// maxIncidentsListed bounds the incidents listing.
const maxIncidentsListed = 10

func incidentsList(ctx context.Context, svc *Service, cmd *Command) (*CommandResponse, error) {
	if cmd.Subcommand != "" && cmd.Subcommand != subList && cmd.Subcommand != "ls" {
		return ephemeral(fmt.Sprintf(
			"Unknown incidents subcommand: `%s`. Available: `list`.", cmd.Subcommand)), nil
	}

	org, _, err := resolveOrg(ctx, svc, cmd.GuildID)
	if err != nil {
		return ephemeral(notConnectedMessage), nil
	}

	filter := &models.ListIncidentsFilter{OrganizationUID: org.UID, Limit: maxIncidentsListed}

	checkSlug := cmd.Flags["check"]
	if checkSlug == "" && len(cmd.Args) > 0 {
		checkSlug = cmd.Args[0]
	}

	if checkSlug != "" {
		check, checkErr := svc.db.GetCheckByUidOrSlug(ctx, org.UID, checkSlug)
		if checkErr != nil || check == nil {
			return ephemeral(fmt.Sprintf("Check `%s` not found.", checkSlug)), nil
		}

		filter.CheckUIDs = []string{check.UID}
	}

	incidents, _, err := svc.db.ListIncidents(ctx, filter)
	if err != nil {
		slog.ErrorContext(ctx, "Discord: failed to list incidents", "error", err)

		return ephemeral("Failed to list incidents."), nil
	}

	if len(incidents) == 0 {
		return reply("No incidents found."), nil
	}

	return reply(fmt.Sprintf("**%d incidents**\n%s",
		len(incidents), strings.Join(incidentLines(ctx, svc, org.UID, incidents), "\n"))), nil
}

func incidentLines(
	ctx context.Context, svc *Service, orgUID string, incidents []*models.Incident,
) []string {
	lines := make([]string, 0, len(incidents))

	for _, incident := range incidents {
		slug := ""
		if check, err := svc.db.GetCheck(ctx, orgUID, incident.CheckUID); err == nil && check.Slug != nil {
			slug = *check.Slug
		}

		if incident.State == models.IncidentStateResolved {
			lines = append(lines, fmt.Sprintf("• `%s` 🟢 resolved %s ago",
				slug, timeutils.FormatHumanReadable(time.Since(derefTime(incident.ResolvedAt)))))

			continue
		}

		lines = append(lines, fmt.Sprintf("• `%s` 🔴 down for %s",
			slug, timeutils.FormatHumanReadable(time.Since(incident.StartedAt))))
	}

	return lines
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Now()
	}

	return *t
}

// configDefaultChannel implements `config default-channel #channel`.
func configDefaultChannel(ctx context.Context, svc *Service, cmd *Command) (*CommandResponse, error) {
	if len(cmd.Args) == 0 {
		return ephemeral("Usage: `/solidping config default-channel #channel`"), nil
	}

	channelID := ParseChannelReference(cmd.Args[0])
	if channelID == "" {
		return ephemeral("That does not look like a channel. Usage: `/solidping config default-channel #channel`"), nil
	}

	if err := svc.SetDefaultChannel(ctx, cmd.GuildID, channelID, true); err != nil {
		slog.WarnContext(ctx, "Discord: failed to set default channel",
			"guild_id", cmd.GuildID, "channel_id", channelID, "error", err)

		return ephemeral("Could not set that channel. It must be a text channel of this server " +
			"that the SolidPing bot can see."), nil
	}

	return reply(fmt.Sprintf("✅ Alerts will go to <#%s> by default.", channelID)), nil
}

// ParseChannelReference extracts a channel id from a `<#123>` mention or a
// bare id.
//
// A plain `#name` yields "" and is refused: resolving a name would mean picking
// one of possibly several channels by guess, and the failure mode of guessing
// wrong is that alerts go somewhere nobody is watching. Note that this function
// is NOT the authorization gate — SetDefaultChannel still proves the id belongs
// to the invoking guild before storing it.
func ParseChannelReference(ref string) string {
	text := strings.TrimSpace(ref)

	if strings.HasPrefix(text, "<#") && strings.HasSuffix(text, ">") {
		return strings.TrimSpace(text[2 : len(text)-1])
	}

	if strings.HasPrefix(text, "#") {
		return ""
	}

	return text
}

// commentUsage is shown for a `comment` with no text.
const commentUsage = "*Usage:* `/solidping comment [#N] <text>`\n" +
	"Inside an incident thread the number is optional."

// commentCommand implements `comment [#N] <text>`.
//
// Resolution order, and it matters:
//  1. An explicit `#N` always wins — someone who names the incident gets that
//     incident or an error, never a silent substitution.
//  2. Otherwise, the thread the command was typed in. Discord gives a command
//     the thread's own channel id, so the incident is a single reverse-mapping
//     lookup away — no channel scan and no ambiguity.
//  3. Otherwise an error. Commenting on the wrong incident is silent failure:
//     the note lands where nobody handling the real outage will read it.
func commentCommand(ctx context.Context, svc *Service, cmd *Command) (*CommandResponse, error) {
	ref, text := parseCommentArgs(strings.Join(cmd.Args, " "))
	if text == "" {
		return ephemeral(commentUsage), nil
	}

	org, _, err := resolveOrg(ctx, svc, cmd.GuildID)
	if err != nil {
		return ephemeral(notConnectedMessage), nil
	}

	incident, problem := resolveCommentIncident(ctx, svc, org.UID, cmd, ref)
	if problem != "" {
		return ephemeral(problem), nil
	}

	if _, err := svc.incidentsService.AddCommentFromDiscordCommand(
		ctx, org.UID, incident.UID, text, cmd.UserID, cmd.UserName, cmd.GuildID,
	); err != nil {
		slog.ErrorContext(ctx, "Discord: failed to add comment",
			"incident_uid", incident.UID, "error", err)

		return ephemeral("Could not add the comment: " + err.Error()), nil
	}

	return ephemeral("💬 Comment added to " + IncidentLabel(incident) + "."), nil
}

// parseCommentArgs splits "#42 restarting the pod" into ("42", "restarting the
// pod"). A leading token is only a reference when it is `#` followed by digits,
// so "#ops please look" stays plain text.
func parseCommentArgs(raw string) (string, string) {
	text := strings.TrimSpace(raw)
	if !strings.HasPrefix(text, "#") {
		return "", text
	}

	head, tail, _ := strings.Cut(text[1:], " ")
	if head == "" {
		return "", text
	}

	if _, err := strconv.ParseInt(head, 10, 64); err != nil {
		return "", text
	}

	return head, strings.TrimSpace(tail)
}

// resolveCommentIncident picks the incident a `comment` refers to. The second
// return is user-facing text, empty on success.
func resolveCommentIncident(
	ctx context.Context, svc *Service, orgUID string, cmd *Command, ref string,
) (*models.Incident, string) {
	if ref != "" {
		number, err := strconv.ParseInt(ref, 10, 64)
		if err != nil {
			return nil, fmt.Sprintf("`#%s` is not an incident number.\n%s", ref, commentUsage)
		}

		incident, err := svc.db.GetIncidentByNumber(ctx, orgUID, number)
		if err != nil || incident == nil {
			return nil, fmt.Sprintf("Incident `#%s` was not found in this server's organization.", ref)
		}

		return incident, ""
	}

	threadID := cmd.ThreadID
	if threadID == "" {
		threadID = cmd.ChannelID
	}

	incidentUID, entryOrgUID, ok := LookupThreadIncident(ctx, svc.db, cmd.GuildID, threadID)
	if !ok || entryOrgUID != orgUID {
		return nil, "This is not a SolidPing incident thread — name the incident:\n" + commentUsage
	}

	incident, err := svc.db.GetIncident(ctx, orgUID, incidentUID)
	if err != nil || incident == nil {
		return nil, "That incident is no longer available."
	}

	return incident, ""
}

// IncidentLabel renders "#42 (api is down)", degrading gracefully for an
// incident created before the per-org numbers existed.
func IncidentLabel(incident *models.Incident) string {
	ref := "the incident"
	if incident.Number > 0 {
		ref = fmt.Sprintf("#%d", incident.Number)
	}

	title := ""
	if incident.Title != nil {
		title = strings.TrimSpace(*incident.Title)
	}

	if title == "" {
		return ref
	}

	return ref + " (" + title + ")"
}

// helpText is the command reference, shown for `help` and for an empty mention.
func helpText() string {
	return strings.Join([]string{
		"**SolidPing commands**",
		"• `/solidping checks list` — list monitored checks",
		"• `/solidping checks add <url>` — start monitoring a URL",
		"• `/solidping checks rm <slug>` — stop monitoring a check",
		"• `/solidping incidents list [check]` — recent incidents",
		"• `/solidping comment [#N] <text>` — add a note to an incident",
		"• `/solidping config default-channel #channel` — where alerts go",
		"• `/solidping help` — this message",
		"",
		"You can also mention the bot: `@SolidPing checks list`.",
	}, "\n")
}
