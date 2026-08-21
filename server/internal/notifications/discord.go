package notifications

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/discord"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

var (
	// ErrDiscordWebhookURLNotConfigured is returned when a legacy webhook-mode
	// integration has no webhook URL.
	ErrDiscordWebhookURLNotConfigured = errors.New("discord webhook URL not configured")
	// ErrDiscordBotNotConfigured is returned when a bot-mode integration is
	// asked to send but the instance has no Discord bot token.
	ErrDiscordBotNotConfigured = errors.New("discord bot token is not configured on this instance")
	// ErrDiscordNoDestination is returned when the bot is installed in a guild
	// but no channel has been chosen yet.
	ErrDiscordNoDestination = errors.New("no default channel configured for discord connection")
)

// State keys for the incident → Discord message/thread mapping.
const (
	discordKeyChannelID = "channel_id"
	discordKeyMessageID = "message_id"
	discordKeyThreadID  = "thread_id"
)

// DiscordSender sends notifications to Discord.
//
// It covers two modes, chosen per integration by the data (see
// models.DiscordSettings):
//
//   - Bot mode (guild + channel set): rich embeds with an Acknowledge button,
//     a real thread per incident, and an in-place edit of the original embed
//     when the incident resolves — the Slack behavior, on Discord's primitives.
//   - Legacy webhook mode: a plain embed POSTed to a pasted webhook URL. This
//     is what every Discord integration created before the bot existed does,
//     and it is deliberately reachable without ANY bot field being present, so
//     an instance with no bot token configured keeps delivering exactly as it
//     always has.
type DiscordSender struct {
	// newBotClient builds the Discord REST client. Tests override it to point
	// at an httptest fake Discord; nil means the real one.
	newBotClient func(token string) *discord.BotClient
}

// Send sends a notification to Discord.
//
// Mode selection is by data, and bot mode requires BOTH a guild and a
// resolved destination channel. Anything else falls through to the legacy
// webhook path, which is what keeps a pre-bot integration working with no
// migration and no bot token on the instance.
func (ds *DiscordSender) Send(ctx context.Context, jctx *jobdef.JobContext, payload *Payload) error {
	settings, err := ds.parseSettings(payload)
	if err != nil {
		return err
	}

	channel := ds.determineChannel(settings, payload)
	if settings.GuildID != "" && channel != "" {
		return ds.sendViaBot(ctx, jctx, settings, payload, channel)
	}

	if settings.WebhookURL == "" {
		if settings.GuildID != "" {
			return ErrDiscordNoDestination
		}

		return ErrDiscordWebhookURLNotConfigured
	}

	return ds.sendViaWebhook(ctx, settings, payload)
}

// parseSettings extracts the Discord settings from the payload.
func (ds *DiscordSender) parseSettings(payload *Payload) (*models.DiscordSettings, error) {
	settings, err := models.DiscordSettingsFromJSONMap(payload.Integration.Settings)
	if err != nil {
		return nil, fmt.Errorf("parsing discord settings: %w", err)
	}

	return settings, nil
}

// determineChannel resolves the destination channel, applying a per-check
// override when one is set.
func (ds *DiscordSender) determineChannel(settings *models.DiscordSettings, payload *Payload) string {
	channel := settings.ChannelID

	if payload.CheckConnectionSettings != nil {
		if override, ok := (*payload.CheckConnectionSettings)[discordKeyChannelID].(string); ok && override != "" {
			channel = override
		}
	}

	return channel
}

// ---------------------------------------------------------------------------
// Legacy webhook mode
// ---------------------------------------------------------------------------

// sendViaWebhook is the pre-bot delivery path, unchanged in behavior.
func (ds *DiscordSender) sendViaWebhook(
	ctx context.Context, settings *models.DiscordSettings, payload *Payload,
) error {
	client := discord.NewClient(settings.WebhookURL)
	msg := &discord.WebhookMessage{
		Username: productName,
		Embeds:   []discord.Embed{ds.buildEmbed(payload)},
	}

	if err := client.SendWebhookMessage(ctx, msg); err != nil {
		return fmt.Errorf("sending discord webhook message: %w", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Bot mode
// ---------------------------------------------------------------------------

// botClient builds the REST client for this send.
func (ds *DiscordSender) botClient(token string) *discord.BotClient {
	if ds.newBotClient != nil {
		return ds.newBotClient(token)
	}

	return discord.NewBotClient(token)
}

// botToken reads the instance-level bot token off the job context.
func (ds *DiscordSender) botToken(jctx *jobdef.JobContext) string {
	if jctx == nil || jctx.AppConfig == nil {
		return ""
	}

	return jctx.AppConfig.Discord.BotToken
}

// discordThreadStateKey is the forward incident→message/thread entry key.
func discordThreadStateKey(incidentUID string) string {
	return "incidents/" + incidentUID + "/discord/thread"
}

// sendViaBot is the bot-mode delivery path.
func (ds *DiscordSender) sendViaBot(
	ctx context.Context,
	jctx *jobdef.JobContext,
	settings *models.DiscordSettings,
	payload *Payload,
	channelID string,
) error {
	token := ds.botToken(jctx)
	if token == "" {
		return ErrDiscordBotNotConfigured
	}

	stateKey := discordThreadStateKey(payload.Incident.UID)

	entry, err := jctx.DBService.GetStateEntry(ctx, &payload.Incident.OrganizationUID, stateKey)
	if err != nil {
		return fmt.Errorf("getting discord thread state entry: %w", err)
	}

	hasThread := entry != nil && entry.Value != nil

	// Same orphan guard as Slack: a resolved/reopened/comment event with no
	// recorded message has nothing to update or thread under, so posting it
	// top-level would be a context-free message — and, for a comment, would
	// steal the incident's thread mapping from the alert that follows.
	if requiresExistingThread(payload.EventType) && !hasThread {
		return nil
	}

	client := ds.botClient(token)

	if hasThread {
		return ds.sendFollowUp(ctx, client, entry, payload)
	}

	return ds.postIncidentMessage(ctx, jctx, client, settings, payload, channelID, stateKey)
}

// postIncidentMessage posts the top-level incident message, opens its thread
// and records both the forward and reverse mappings.
func (ds *DiscordSender) postIncidentMessage(
	ctx context.Context,
	jctx *jobdef.JobContext,
	client *discord.BotClient,
	settings *models.DiscordSettings,
	payload *Payload,
	channelID, stateKey string,
) error {
	msg := ds.buildBotMessage(payload, settings)

	result, err := client.CreateMessage(ctx, channelID, msg)
	if err != nil {
		return fmt.Errorf("posting discord message: %w", err)
	}

	payload.MessageID = result.ID

	threadID := ds.openThread(ctx, client, channelID, result.ID, payload)

	return ds.storeThreadInfo(ctx, jctx, settings, payload, stateKey, channelID, result.ID, threadID)
}

// openThread creates the incident's discussion thread.
//
// Eager rather than lazy: on Discord a thread must be created explicitly, and
// creating it up front is what gives a responder somewhere to reply the moment
// the alert lands — which is the only way inbound comment ingestion can work
// for the first reply. Best-effort: a guild that denies the thread permission
// still gets the alert, and follow-ups then land in the channel instead.
func (ds *DiscordSender) openThread(
	ctx context.Context, client *discord.BotClient, channelID, messageID string, payload *Payload,
) string {
	name := discordThreadName(payload)

	thread, err := client.StartThreadFromMessage(ctx, channelID, messageID, name)
	if err != nil {
		slog.WarnContext(ctx, "Could not open Discord incident thread — follow-ups will post in the channel",
			"incident_uid", payload.Incident.UID, "channel_id", channelID, "error", err)

		return ""
	}

	return thread.ID
}

// discordThreadName labels the incident thread the way every other surface
// names an incident, so a reader recognizes it without translation.
func discordThreadName(payload *Payload) string {
	return strings.TrimSpace(incidentRefPrefix(payload.Incident) + getCheckName(payload.Check))
}

// storeThreadInfo writes the forward incident→message entry (used to edit the
// original embed and reply into the thread) and, alongside it, the reverse
// thread→incident entry that lets an inbound Discord thread reply resolve back
// to this incident in one lookup.
func (ds *DiscordSender) storeThreadInfo(
	ctx context.Context,
	jctx *jobdef.JobContext,
	settings *models.DiscordSettings,
	payload *Payload,
	stateKey, channelID, messageID, threadID string,
) error {
	value := &models.JSONMap{
		discordKeyChannelID: channelID,
		discordKeyMessageID: messageID,
		discordKeyThreadID:  threadID,
	}

	if err := jctx.DBService.SetStateEntry(ctx, &payload.Incident.OrganizationUID, stateKey, value, nil); err != nil {
		return fmt.Errorf("storing discord thread state entry: %w", err)
	}

	ds.storeReverseThreadInfo(ctx, jctx, settings, payload, threadID)

	return nil
}

// storeReverseThreadInfo writes the reverse thread→incident mapping as a global
// (org-nil) state entry so an inbound reply routes to its incident regardless
// of which org is the guild's inbound home org. Best-effort: the outbound
// message is already posted, so a failure here only means inbound replies to
// this thread won't be captured.
func (ds *DiscordSender) storeReverseThreadInfo(
	ctx context.Context,
	jctx *jobdef.JobContext,
	settings *models.DiscordSettings,
	payload *Payload,
	threadID string,
) {
	if threadID == "" || settings.GuildID == "" {
		return
	}

	reverseKey := discord.ReverseThreadStateKey(settings.GuildID, threadID)
	reverseValue := &models.JSONMap{
		discord.ThreadIncidentUIDKey: payload.Incident.UID,
		discord.ThreadOrgUIDKey:      payload.Incident.OrganizationUID,
	}

	if err := jctx.DBService.SetStateEntry(ctx, nil, reverseKey, reverseValue, nil); err != nil {
		slog.WarnContext(ctx, "Failed to store reverse Discord thread mapping",
			"incident_uid", payload.Incident.UID, "error", err)
	}
}

// threadRef is the decoded forward state entry.
type threadRef struct {
	ChannelID string
	MessageID string
	ThreadID  string
}

// decodeThreadRef reads the stored message/thread reference.
func decodeThreadRef(entry *models.StateEntry) threadRef {
	var ref threadRef

	if entry == nil || entry.Value == nil {
		return ref
	}

	ref.ChannelID, _ = (*entry.Value)[discordKeyChannelID].(string)
	ref.MessageID, _ = (*entry.Value)[discordKeyMessageID].(string)
	ref.ThreadID, _ = (*entry.Value)[discordKeyThreadID].(string)

	return ref
}

// sendFollowUp handles every event that lands under an already-posted incident
// message: resolve and reopen also rewrite the original embed in place.
func (ds *DiscordSender) sendFollowUp(
	ctx context.Context,
	client *discord.BotClient,
	entry *models.StateEntry,
	payload *Payload,
) error {
	ref := decodeThreadRef(entry)
	if ref.ChannelID == "" || ref.MessageID == "" {
		return nil
	}

	switch payload.EventType {
	case eventTypeIncidentResolved:
		if err := ds.editOriginal(ctx, client, ref, ds.buildResolvedUpdateMessage(payload)); err != nil {
			return err
		}
	case eventTypeIncidentReopened:
		if err := ds.editOriginal(ctx, client, ref, ds.buildReopenedUpdateMessage(payload)); err != nil {
			return err
		}
	}

	return ds.postThreadReply(ctx, client, ref, payload)
}

// editOriginal rewrites the incident's original message.
func (ds *DiscordSender) editOriginal(
	ctx context.Context, client *discord.BotClient, ref threadRef, msg *discord.Message,
) error {
	if err := client.EditMessage(ctx, ref.ChannelID, ref.MessageID, msg); err != nil {
		return fmt.Errorf("updating discord message: %w", err)
	}

	return nil
}

// postThreadReply posts the follow-up into the incident's thread, un-archiving
// it first.
//
// The un-archive is what makes a LATE follow-up work at all: Discord
// auto-archives a thread after its inactivity window (1 day to 1 week), and a
// post into an archived thread is rejected. An incident that runs longer than
// the window — exactly the incident whose resolve message matters most — would
// otherwise resolve silently.
//
// With no thread (creation was denied at post time) the reply falls back to the
// channel, which is noisier but never silent.
func (ds *DiscordSender) postThreadReply(
	ctx context.Context, client *discord.BotClient, ref threadRef, payload *Payload,
) error {
	target := ref.ThreadID
	if target == "" {
		target = ref.ChannelID
	} else if err := client.UnarchiveThread(ctx, target); err != nil {
		slog.WarnContext(ctx, "Could not un-archive Discord incident thread",
			"incident_uid", payload.Incident.UID, "thread_id", target, "error", err)
	}

	reply := &discord.Message{
		Embeds:     []discord.Embed{ds.buildEmbed(payload)},
		Components: []discord.Component{},
	}

	result, err := client.CreateMessage(ctx, target, reply)
	if err != nil {
		return fmt.Errorf("posting discord thread reply: %w", err)
	}

	payload.MessageID = result.ID

	return nil
}

// ---------------------------------------------------------------------------
// Message building
// ---------------------------------------------------------------------------

// buildBotMessage builds the top-level incident message: the embed, the
// on-call mention line, and the action buttons.
func (ds *DiscordSender) buildBotMessage(payload *Payload, settings *models.DiscordSettings) *discord.Message {
	msg := &discord.Message{
		Embeds:     []discord.Embed{ds.buildEmbed(payload)},
		Components: []discord.Component{},
	}

	if settings.MentionOnCall {
		content, userIDs := renderDiscordMentions(payload.OnCallMentions)
		if content != "" {
			msg.Content = content
			// Bound what the message may ping: an empty `parse` disables
			// @everyone/@here/role pings entirely, and only the users we
			// actually resolved are allowed through.
			msg.AllowedMentions = &discord.AllowedMentions{Parse: []string{}, Users: userIDs}
		}
	}

	if actionable(payload.EventType) {
		msg.Components = []discord.Component{discord.IncidentActionRow(payload.Incident.UID)}
	}

	return msg
}

// actionable reports whether an event's message carries incident buttons.
func actionable(eventType string) bool {
	return eventType == eventTypeIncidentCreated ||
		eventType == eventTypeIncidentEscalated ||
		eventType == eventTypeIncidentReopened
}

// renderDiscordMentions turns the resolved on-call targets into a Discord
// mention line plus the explicit allow-list of user ids it may ping.
//
// A target with an identity renders as `<@123>` (a real ping); one without
// renders as its plain-text name, which names the responsible person without
// notifying anyone — the deliberate degradation when a member has no mapping.
func renderDiscordMentions(targets []MentionTarget) (string, []string) {
	if len(targets) == 0 {
		return "", nil
	}

	parts := make([]string, 0, len(targets))
	ids := make([]string, 0, len(targets))

	for i := range targets {
		if targets[i].ExternalID != "" {
			parts = append(parts, "<@"+targets[i].ExternalID+">")
			ids = append(ids, targets[i].ExternalID)

			continue
		}

		if targets[i].DisplayName != "" {
			parts = append(parts, targets[i].DisplayName)
		}
	}

	if len(parts) == 0 {
		return "", nil
	}

	return strings.Join(parts, " ") + " — you are on call for this.", ids
}

// buildEmbed builds the embed for an event.
func (ds *DiscordSender) buildEmbed(payload *Payload) discord.Embed {
	switch payload.EventType {
	case eventTypeIncidentCreated:
		return ds.buildIncidentCreatedEmbed(payload)
	case eventTypeIncidentResolved:
		return ds.buildIncidentResolvedEmbed(payload)
	case eventTypeIncidentEscalated:
		return ds.buildIncidentEscalatedEmbed(payload)
	case eventTypeIncidentReopened:
		return ds.buildIncidentReopenedEmbed(payload)
	case eventTypeIncidentComment:
		return ds.buildCommentEmbed(payload)
	default:
		return ds.buildDefaultEmbed(payload)
	}
}

// buildIncidentCreatedEmbed builds an embed for incident.created events.
func (ds *DiscordSender) buildIncidentCreatedEmbed(payload *Payload) discord.Embed {
	checkName := getCheckName(payload.Check)
	fields := ds.buildCommonFields(payload, checkName)

	return discord.Embed{
		Title:       incidentRefPrefix(payload.Incident) + "New incident for " + checkName,
		Description: "A new incident has been detected. Please investigate.",
		URL:         incidentDashURL(payload.AppBaseURL, payload.OrgSlug, payload.Incident),
		Color:       discord.ColorRed,
		Fields:      fields,
		Timestamp:   payload.Incident.StartedAt.Format(time.RFC3339),
		Footer:      &discord.Footer{Text: productMonitoring},
	}
}

// buildCommentEmbed builds an embed for incident.comment events.
func (ds *DiscordSender) buildCommentEmbed(payload *Payload) discord.Embed {
	checkName := getCheckName(payload.Check)

	fields := []discord.Field{
		{Name: fieldLabelMonitor, Value: checkName, Inline: true},
		{Name: fieldLabelAuthor, Value: commentAuthor(payload.Comment), Inline: true},
	}

	return discord.Embed{
		Title:       commentTitle(payload),
		Description: commentText(payload.Comment),
		Color:       discord.ColorBlue,
		Fields:      fields,
		Timestamp:   time.Now().Format(time.RFC3339),
		Footer:      &discord.Footer{Text: productMonitoring},
	}
}

// buildIncidentResolvedEmbed builds an embed for incident.resolved events.
func (ds *DiscordSender) buildIncidentResolvedEmbed(payload *Payload) discord.Embed {
	checkName := getCheckName(payload.Check)

	fields := []discord.Field{
		{Name: fieldLabelMonitor, Value: checkName, Inline: true},
	}

	if payload.Incident.ResolvedAt != nil {
		duration := payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt)
		fields = append(fields, discord.Field{
			Name:   mmFieldDuration,
			Value:  formatDuration(duration),
			Inline: true,
		})
	}

	return discord.Embed{
		Title:       incidentRefPrefix(payload.Incident) + "Incident resolved for " + checkName,
		Description: "The incident has been automatically resolved.",
		Color:       discord.ColorGreen,
		Fields:      fields,
		Timestamp:   time.Now().Format(time.RFC3339),
		Footer:      &discord.Footer{Text: productMonitoring},
	}
}

// buildIncidentEscalatedEmbed builds an embed for incident.escalated events.
func (ds *DiscordSender) buildIncidentEscalatedEmbed(payload *Payload) discord.Embed {
	checkName := getCheckName(payload.Check)
	duration := formatDuration(time.Since(payload.Incident.StartedAt))

	fields := []discord.Field{
		{Name: fieldLabelMonitor, Value: checkName, Inline: true},
		{Name: "Failures", Value: strconv.Itoa(payload.Incident.FailureCount), Inline: true},
		{Name: mmFieldDuration, Value: duration, Inline: true},
	}

	if url := getCheckURL(payload.Check); url != "" {
		method := getCheckMethod(payload.Check)
		fields = append(fields, discord.Field{
			Name:  mmFieldCheck,
			Value: fmt.Sprintf("%s `%s`", method, url),
		})
	}

	return discord.Embed{
		Title:       incidentRefPrefix(payload.Incident) + "Incident escalated: " + checkName,
		Description: "This incident has exceeded the escalation threshold.",
		Color:       discord.ColorOrange,
		Fields:      fields,
		Timestamp:   time.Now().Format(time.RFC3339),
		Footer:      &discord.Footer{Text: productMonitoring},
	}
}

// buildIncidentReopenedEmbed builds an embed for incident.reopened events.
func (ds *DiscordSender) buildIncidentReopenedEmbed(payload *Payload) discord.Embed {
	checkName := getCheckName(payload.Check)

	fields := ds.buildCommonFields(payload, checkName)
	fields = append(fields, discord.Field{
		Name:   "Relapse",
		Value:  fmt.Sprintf("#%d", payload.Incident.RelapseCount),
		Inline: true,
	})

	return discord.Embed{
		Title: fmt.Sprintf(
			"%sIncident reopened for %s (relapse #%d)",
			incidentRefPrefix(payload.Incident), checkName, payload.Incident.RelapseCount,
		),
		Description: fmt.Sprintf(
			"Recovery requires the check to stay up for %d seconds.",
			payload.Check.RecoveryPeriodSeconds,
		),
		Color:     discord.ColorRed,
		Fields:    fields,
		Timestamp: time.Now().Format(time.RFC3339),
		Footer:    &discord.Footer{Text: productMonitoring},
	}
}

// buildDefaultEmbed builds a default embed for unknown event types.
func (ds *DiscordSender) buildDefaultEmbed(payload *Payload) discord.Embed {
	checkName := getCheckName(payload.Check)

	return discord.Embed{
		Title:       "Incident update for " + checkName,
		Description: "An incident update occurred.",
		Color:       discord.ColorBlue,
		Timestamp:   time.Now().Format(time.RFC3339),
		Footer:      &discord.Footer{Text: productMonitoring},
	}
}

// buildCommonFields builds fields common to created and reopened embeds.
func (ds *DiscordSender) buildCommonFields(payload *Payload, checkName string) []discord.Field {
	fields := []discord.Field{
		{Name: fieldLabelMonitor, Value: checkName, Inline: true},
		{Name: "Cause", Value: getFailureReason(payload.Incident), Inline: true},
	}

	if url := getCheckURL(payload.Check); url != "" {
		method := getCheckMethod(payload.Check)
		fields = append(fields, discord.Field{
			Name:  mmFieldCheck,
			Value: fmt.Sprintf("%s `%s`", method, url),
		})
	}

	return fields
}

// buildResolvedUpdateMessage is what the ORIGINAL incident message becomes once
// the incident resolves: a green card with no buttons, so the channel does not
// keep a stale red alert with a live Acknowledge button on it.
func (ds *DiscordSender) buildResolvedUpdateMessage(payload *Payload) *discord.Message {
	checkName := getCheckName(payload.Check)

	duration := ""
	if payload.Incident.ResolvedAt != nil {
		duration = formatDuration(payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt))
	}

	fields := []discord.Field{
		{Name: fieldLabelMonitor, Value: checkName, Inline: true},
		{Name: "Cause", Value: getFailureReason(payload.Incident), Inline: true},
	}

	if duration != "" {
		fields = append(fields, discord.Field{Name: "Length", Value: duration, Inline: true})
	}

	if url := getCheckURL(payload.Check); url != "" {
		fields = append(fields, discord.Field{
			Name:  mmFieldCheck,
			Value: fmt.Sprintf("%s `%s`", getCheckMethod(payload.Check), url),
		})
	}

	return &discord.Message{
		Embeds: []discord.Embed{{
			Title: fmt.Sprintf("%sAutomatically resolved %s incident",
				incidentRefPrefix(payload.Incident), checkName),
			URL:       incidentDashURL(payload.AppBaseURL, payload.OrgSlug, payload.Incident),
			Color:     discord.ColorGreen,
			Fields:    fields,
			Timestamp: time.Now().Format(time.RFC3339),
			Footer:    &discord.Footer{Text: productMonitoring},
		}},
		// Explicitly empty (not omitted): this is what CLEARS the Acknowledge
		// button from the resolved card.
		Components: []discord.Component{},
	}
}

// buildReopenedUpdateMessage restores the original message to an active state.
func (ds *DiscordSender) buildReopenedUpdateMessage(payload *Payload) *discord.Message {
	return &discord.Message{
		Embeds:     []discord.Embed{ds.buildIncidentReopenedEmbed(payload)},
		Components: []discord.Component{discord.IncidentActionRow(payload.Incident.UID)},
	}
}
