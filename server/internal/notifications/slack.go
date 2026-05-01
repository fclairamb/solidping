package notifications

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/slack"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

var (
	// ErrSlackAccessTokenNotConfigured is returned when the Slack access token is missing.
	ErrSlackAccessTokenNotConfigured = errors.New("slack access token not configured")
	// ErrNoDefaultChannelConfigured is returned when no default channel is configured for Slack.
	ErrNoDefaultChannelConfigured = errors.New("no default channel configured for slack connection")
)

// Storage keys for Slack thread state.
const (
	slackKeyChannelID = "channel_id"
	slackKeyMessageID = "message_id"
	slackKeyThreadTS  = "thread_ts"
)

// SlackSender sends notifications via Slack.
type SlackSender struct{}

// Send sends a notification to Slack.
func (s *SlackSender) Send(ctx context.Context, jctx *jobdef.JobContext, payload *Payload) error {
	settings, channel, err := s.parseSettings(payload)
	if err != nil {
		return err
	}

	stateKey := "incidents/" + payload.Incident.UID + "/slack/thread"
	threadEntry, err := jctx.DBService.GetStateEntry(ctx, &payload.Incident.OrganizationUID, stateKey)
	if err != nil {
		return fmt.Errorf("getting thread state entry: %w", err)
	}

	client := slack.NewClient(settings.AccessToken)

	// Handle incident resolution - update the original message AND post a thread reply
	if payload.EventType == eventTypeIncidentResolved && threadEntry != nil && threadEntry.Value != nil {
		return s.handleIncidentResolution(ctx, client, threadEntry, payload)
	}

	// Handle incident reopen - update the original message back to active AND post a thread reply
	if payload.EventType == eventTypeIncidentReopened && threadEntry != nil && threadEntry.Value != nil {
		return s.handleIncidentReopen(ctx, client, threadEntry, payload)
	}

	return s.postNewMessage(ctx, jctx, client, payload, channel, stateKey, threadEntry)
}

// parseSettings extracts and validates Slack settings from the payload.
func (s *SlackSender) parseSettings(payload *Payload) (*models.SlackSettings, string, error) {
	settings, err := models.SlackSettingsFromJSONMap(payload.Connection.Settings)
	if err != nil {
		return nil, "", fmt.Errorf("parsing slack settings: %w", err)
	}

	if settings.AccessToken == "" {
		return nil, "", ErrSlackAccessTokenNotConfigured
	}

	channel := s.determineChannel(settings, payload)
	if channel == "" {
		return nil, "", ErrNoDefaultChannelConfigured
	}

	return settings, channel, nil
}

// determineChannel determines the target channel, applying overrides if present.
func (s *SlackSender) determineChannel(settings *models.SlackSettings, payload *Payload) string {
	channel := settings.ChannelID
	if payload.CheckConnectionSettings != nil {
		if override, ok := (*payload.CheckConnectionSettings)[slackKeyChannelID].(string); ok && override != "" {
			channel = override
		}
	}

	return channel
}

// postNewMessage posts a new message or thread reply.
func (s *SlackSender) postNewMessage(
	ctx context.Context, jctx *jobdef.JobContext, client *slack.Client,
	payload *Payload, channel, stateKey string, threadEntry *models.StateEntry,
) error {
	msg := s.buildMessage(payload)
	opts := slack.PostMessageOptions{Channel: channel, Message: msg}

	if threadEntry != nil && threadEntry.Value != nil {
		if ts, ok := (*threadEntry.Value)[slackKeyThreadTS].(string); ok && ts != "" {
			opts.ThreadTS = ts
		}
	}

	result, err := client.PostMessage(ctx, opts)
	if err != nil {
		return fmt.Errorf("posting slack message: %w", err)
	}

	if opts.ThreadTS == "" {
		return s.storeThreadInfo(ctx, jctx, payload, stateKey, result)
	}

	return nil
}

// storeThreadInfo stores the thread information for future replies.
func (s *SlackSender) storeThreadInfo(
	ctx context.Context, jctx *jobdef.JobContext, payload *Payload, stateKey string, result *slack.PostMessageResult,
) error {
	value := &models.JSONMap{
		slackKeyChannelID: result.Channel,
		slackKeyMessageID: result.TS,
		slackKeyThreadTS:  result.TS,
	}

	if err := jctx.DBService.SetStateEntry(ctx, &payload.Incident.OrganizationUID, stateKey, value, nil); err != nil {
		return fmt.Errorf("storing thread state entry: %w", err)
	}

	return nil
}

func (s *SlackSender) buildMessage(payload *Payload) *slack.MessageResponse {
	switch payload.EventType {
	case eventTypeIncidentCreated:
		return s.buildIncidentCreatedMessage(payload)
	case eventTypeIncidentResolved:
		return s.buildIncidentResolvedThreadReply(payload)
	case eventTypeIncidentEscalated:
		return s.buildIncidentEscalatedMessage(payload)
	case eventTypeIncidentReopened:
		return s.buildIncidentReopenedThreadReply(payload)
	default:
		return s.buildSimpleMessage(payload)
	}
}

// getCheckName returns the check name from Name or Slug.
func getCheckName(check *models.Check) string {
	if check.Name != nil && *check.Name != "" {
		return *check.Name
	}
	if check.Slug != nil && *check.Slug != "" {
		return *check.Slug
	}
	return "Unknown check"
}

// getCheckURL returns the URL from the check config for HTTP checks.
func getCheckURL(check *models.Check) string {
	if check.Config == nil {
		return ""
	}
	if url, ok := check.Config["url"].(string); ok {
		return url
	}
	return ""
}

// getCheckMethod returns the HTTP method from the check config.
func getCheckMethod(check *models.Check) string {
	if check.Config == nil {
		return "GET"
	}
	if method, ok := check.Config["method"].(string); ok && method != "" {
		return method
	}
	return "GET"
}

// getFailureReason returns a human-readable failure reason from the incident details.
func getFailureReason(incident *models.Incident) string {
	if incident.Details == nil {
		return "Check failed"
	}
	if reason, ok := incident.Details["failure_reason"].(string); ok && reason != "" {
		return reason
	}
	if output, ok := incident.Details["output"].(string); ok && output != "" {
		return output
	}
	return "Check failed"
}

// formatDuration formats a duration into a human-readable string.
func formatDuration(dur time.Duration) string {
	dur = dur.Round(time.Second)
	if dur < time.Minute {
		return fmt.Sprintf("%d seconds", int(dur.Seconds()))
	}
	if dur < time.Hour {
		mins := int(dur.Minutes())
		if mins == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", mins)
	}
	hours := int(dur.Hours())
	mins := int(dur.Minutes()) % 60
	if hours == 1 {
		if mins == 0 {
			return "1 hour"
		}
		return fmt.Sprintf("1 hour %d minutes", mins)
	}
	if mins == 0 {
		return fmt.Sprintf("%d hours", hours)
	}
	return fmt.Sprintf("%d hours %d minutes", hours, mins)
}

// formatTimestamp formats a time for display in Slack.
func formatTimestamp(t time.Time) string {
	return t.Format("today at 3:04:05 PM")
}

// Slack attachment colors for different incident states.
const (
	colorDanger  = "#D32F2F" // Red for active incidents
	colorWarning = "#FF9800" // Orange for escalations
	colorSuccess = "#4CAF50" // Green for resolved
)

// buildIncidentCreatedMessage builds a rich Block Kit message for incident.created events.
func (s *SlackSender) buildIncidentCreatedMessage(payload *Payload) *slack.MessageResponse {
	checkName := getCheckName(payload.Check)
	fallbackText := "New incident for " + checkName
	fields := s.buildIncidentFields(payload, checkName)
	blocks := s.buildIncidentCreatedBlocks(payload, checkName, fields)

	return &slack.MessageResponse{
		Text: fallbackText,
		Attachments: []slack.Attachment{
			{Color: colorDanger, Fallback: fallbackText, Blocks: blocks},
		},
	}
}

// buildIncidentFields builds the common section fields for incident messages.
func (s *SlackSender) buildIncidentFields(payload *Payload, checkName string) []slack.Text {
	fields := []slack.Text{
		{Type: slack.BlockTypeMrkdwn, Text: "*Monitor:*\n" + checkName},
		{Type: slack.BlockTypeMrkdwn, Text: "*Cause:*\n" + getFailureReason(payload.Incident)},
	}

	if url := getCheckURL(payload.Check); url != "" {
		method := getCheckMethod(payload.Check)
		fields = append(fields, slack.Text{
			Type: slack.BlockTypeMrkdwn,
			Text: fmt.Sprintf("*Check:*\n%s `%s`", method, url),
		})
	}

	return fields
}

// buildIncidentCreatedBlocks builds the blocks for incident.created messages.
func (s *SlackSender) buildIncidentCreatedBlocks(
	payload *Payload, checkName string, fields []slack.Text,
) []slack.Block {
	return []slack.Block{
		{
			Type: slack.BlockTypeHeader,
			Text: &slack.Text{Type: slack.BlockTypePlainText, Text: "New incident for " + checkName, Emoji: true},
		},
		{Type: slack.BlockTypeSection, Fields: fields},
		{
			Type: slack.BlockTypeSection,
			Text: &slack.Text{Type: slack.BlockTypeMrkdwn, Text: "Please acknowledge the incident."},
		},
		s.buildIncidentActionButtons(payload.Incident.UID),
		{
			Type: slack.BlockTypeContext,
			Elements: []any{
				slack.ContextElement{
					Type: slack.BlockTypeMrkdwn,
					Text: ":warning: Incident  :large_blue_circle: Monitor",
				},
			},
		},
		{
			Type: slack.BlockTypeContext,
			Elements: []any{
				slack.ContextElement{
					Type: slack.BlockTypeMrkdwn,
					Text: "Incident started " + formatTimestamp(payload.Incident.StartedAt),
				},
			},
		},
	}
}

// buildIncidentActionButtons builds the action buttons block for incident messages.
func (s *SlackSender) buildIncidentActionButtons(incidentUID string) slack.Block {
	return slack.Block{
		Type:    "actions",
		BlockID: "incident_actions",
		Elements: []any{
			slack.Element{
				Type: slack.BlockTypeButton, ActionID: "acknowledge_incident", Value: incidentUID, Style: "primary",
				Text: &slack.Text{Type: slack.BlockTypePlainText, Text: "Acknowledge", Emoji: true},
			},
			slack.Element{
				Type: slack.BlockTypeButton, ActionID: "unavailable_incident", Value: incidentUID,
				Text: &slack.Text{Type: slack.BlockTypePlainText, Text: "I'm unavailable", Emoji: true},
			},
			slack.Element{
				Type: slack.BlockTypeButton, ActionID: "escalate_incident", Value: incidentUID, Style: "danger",
				Text: &slack.Text{Type: slack.BlockTypePlainText, Text: "Escalate", Emoji: true},
			},
		},
	}
}

// buildIncidentResolvedThreadReply builds a simple thread reply for resolved incidents.
func (s *SlackSender) buildIncidentResolvedThreadReply(payload *Payload) *slack.MessageResponse {
	duration := ""
	if payload.Incident.ResolvedAt != nil {
		d := payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt)
		duration = formatDuration(d)
	}

	text := fmt.Sprintf(":white_check_mark: Incident resolved after %s.", duration)

	blocks := []slack.Block{
		{
			Type: slack.BlockTypeSection,
			Text: &slack.Text{
				Type: slack.BlockTypeMrkdwn,
				Text: text,
			},
		},
	}

	return &slack.MessageResponse{
		Text: text,
		Attachments: []slack.Attachment{
			{
				Color:    colorSuccess,
				Fallback: text,
				Blocks:   blocks,
			},
		},
	}
}

// buildIncidentEscalatedMessage builds a rich Block Kit message for escalation.
func (s *SlackSender) buildIncidentEscalatedMessage(payload *Payload) *slack.MessageResponse {
	checkName := getCheckName(payload.Check)
	fallbackText := "Incident escalated: " + checkName

	// Calculate duration
	duration := formatDuration(time.Since(payload.Incident.StartedAt))

	// Build section fields
	fields := []slack.Text{
		{Type: slack.BlockTypeMrkdwn, Text: "*Monitor:*\n" + checkName},
		{Type: slack.BlockTypeMrkdwn, Text: fmt.Sprintf("*Failures:*\n%d", payload.Incident.FailureCount)},
		{Type: slack.BlockTypeMrkdwn, Text: "*Duration:*\n" + duration},
	}

	// Add URL field for HTTP checks
	if url := getCheckURL(payload.Check); url != "" {
		method := getCheckMethod(payload.Check)
		fields = append(fields, slack.Text{
			Type: slack.BlockTypeMrkdwn,
			Text: fmt.Sprintf("*Check:*\n%s `%s`", method, url),
		})
	}

	blocks := []slack.Block{
		// Header
		{
			Type: slack.BlockTypeHeader,
			Text: &slack.Text{
				Type:  slack.BlockTypePlainText,
				Text:  ":rotating_light: Incident escalated: " + checkName,
				Emoji: true,
			},
		},
		// Section with fields
		{
			Type:   "section",
			Fields: fields,
		},
		// Explanation
		{
			Type: slack.BlockTypeSection,
			Text: &slack.Text{
				Type: slack.BlockTypeMrkdwn,
				Text: "This incident has exceeded the escalation threshold.",
			},
		},
		// Action buttons
		{
			Type:    "actions",
			BlockID: "escalation_actions",
			Elements: []any{
				slack.Element{
					Type:     "button",
					ActionID: "acknowledge_incident",
					Value:    payload.Incident.UID,
					Style:    "primary",
					Text: &slack.Text{
						Type:  slack.BlockTypePlainText,
						Text:  "Acknowledge",
						Emoji: true,
					},
				},
			},
		},
		// Context: status tags
		{
			Type: slack.BlockTypeContext,
			Elements: []any{
				slack.ContextElement{Type: slack.BlockTypeMrkdwn, Text: ":rotating_light: Escalated  :warning: Incident"},
			},
		},
	}

	return &slack.MessageResponse{
		Text: fallbackText,
		Attachments: []slack.Attachment{
			{
				Color:    colorWarning,
				Fallback: fallbackText,
				Blocks:   blocks,
			},
		},
	}
}

// buildSimpleMessage builds a simple fallback message for unknown event types.
func (s *SlackSender) buildSimpleMessage(payload *Payload) *slack.MessageResponse {
	checkName := getCheckName(payload.Check)
	text := fmt.Sprintf("Incident update for *%s*", checkName)

	return &slack.MessageResponse{
		Text: text,
		Blocks: []slack.Block{
			{
				Type: slack.BlockTypeSection,
				Text: &slack.Text{
					Type: slack.BlockTypeMrkdwn,
					Text: text,
				},
			},
		},
	}
}

// handleIncidentResolution handles updating the original message and posting a thread reply for resolved incidents.
func (s *SlackSender) handleIncidentResolution(
	ctx context.Context, client *slack.Client, threadEntry *models.StateEntry, payload *Payload,
) error {
	messageID, hasMessageID := (*threadEntry.Value)[slackKeyMessageID].(string)
	channelID, hasChannelID := (*threadEntry.Value)[slackKeyChannelID].(string)
	threadTS, hasThreadTS := (*threadEntry.Value)[slackKeyThreadTS].(string)

	if !hasMessageID || messageID == "" || !hasChannelID || channelID == "" {
		return nil
	}

	// 1. Update the original message to show resolved status (inline)
	updateMsg := s.buildResolvedUpdateMessage(payload)
	updateOpts := slack.UpdateMessageOptions{
		Channel: channelID,
		TS:      messageID,
		Message: updateMsg,
	}

	if updateErr := client.UpdateMessage(ctx, updateOpts); updateErr != nil {
		return fmt.Errorf("updating slack message: %w", updateErr)
	}

	// 2. Post a reply in the thread
	if hasThreadTS && threadTS != "" {
		threadReplyMsg := s.buildMessage(payload)
		replyOpts := slack.PostMessageOptions{
			Channel:  channelID,
			ThreadTS: threadTS,
			Message:  threadReplyMsg,
		}

		if _, postErr := client.PostMessage(ctx, replyOpts); postErr != nil {
			return fmt.Errorf("posting thread reply: %w", postErr)
		}
	}

	return nil
}

// buildResolvedUpdateMessage builds the message to update the original incident message
// when the incident is resolved. It replaces the original message with a resolved status.
func (s *SlackSender) buildResolvedUpdateMessage(payload *Payload) *slack.MessageResponse {
	checkName := getCheckName(payload.Check)
	fallbackText := fmt.Sprintf("Automatically resolved %s incident", checkName)

	// Calculate duration
	duration := ""
	if payload.Incident.ResolvedAt != nil {
		d := payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt)
		duration = formatDuration(d)
	}

	// Build section fields
	fields := []slack.Text{
		{Type: slack.BlockTypeMrkdwn, Text: "*Monitor:*\n" + checkName},
		{Type: slack.BlockTypeMrkdwn, Text: "*Cause:*\n" + getFailureReason(payload.Incident)},
		{Type: slack.BlockTypeMrkdwn, Text: "*Length:*\n" + duration},
	}

	// Add URL field for HTTP checks
	if url := getCheckURL(payload.Check); url != "" {
		method := getCheckMethod(payload.Check)
		fields = append(fields, slack.Text{
			Type: slack.BlockTypeMrkdwn,
			Text: "*Checked URL:*\n" + method + " `" + url + "`",
		})
	}

	blocks := []slack.Block{
		// Header with resolved indicator
		{
			Type: slack.BlockTypeHeader,
			Text: &slack.Text{
				Type:  slack.BlockTypePlainText,
				Text:  fmt.Sprintf(":white_check_mark: Automatically resolved %s incident", checkName),
				Emoji: true,
			},
		},
		// Section with fields
		{
			Type:   "section",
			Fields: fields,
		},
		// No action buttons - incident is resolved
		// Context: status tags
		{
			Type: slack.BlockTypeContext,
			Elements: []any{
				slack.ContextElement{Type: slack.BlockTypeMrkdwn, Text: ":white_check_mark: Resolved  :large_blue_circle: Monitor"},
			},
		},
		// Context: timestamp
		{
			Type: slack.BlockTypeContext,
			Elements: []any{
				slack.ContextElement{
					Type: slack.BlockTypeMrkdwn,
					Text: "Incident started " + formatTimestamp(payload.Incident.StartedAt),
				},
			},
		},
	}

	return &slack.MessageResponse{
		Text: fallbackText,
		Attachments: []slack.Attachment{
			{
				Color:    colorSuccess,
				Fallback: fallbackText,
				Blocks:   blocks,
			},
		},
	}
}

// handleIncidentReopen handles updating the original message and posting a thread reply for reopened incidents.
func (s *SlackSender) handleIncidentReopen(
	ctx context.Context, client *slack.Client, threadEntry *models.StateEntry, payload *Payload,
) error {
	messageID, hasMessageID := (*threadEntry.Value)[slackKeyMessageID].(string)
	channelID, hasChannelID := (*threadEntry.Value)[slackKeyChannelID].(string)
	threadTS, hasThreadTS := (*threadEntry.Value)[slackKeyThreadTS].(string)

	if !hasMessageID || messageID == "" || !hasChannelID || channelID == "" {
		return nil
	}

	// 1. Update the original message back to active state
	updateMsg := s.buildReopenedUpdateMessage(payload)
	updateOpts := slack.UpdateMessageOptions{
		Channel: channelID,
		TS:      messageID,
		Message: updateMsg,
	}

	if updateErr := client.UpdateMessage(ctx, updateOpts); updateErr != nil {
		return fmt.Errorf("updating slack message for reopen: %w", updateErr)
	}

	// 2. Post a reply in the thread
	if hasThreadTS && threadTS != "" {
		threadReplyMsg := s.buildIncidentReopenedThreadReply(payload)
		replyOpts := slack.PostMessageOptions{
			Channel:  channelID,
			ThreadTS: threadTS,
			Message:  threadReplyMsg,
		}

		if _, postErr := client.PostMessage(ctx, replyOpts); postErr != nil {
			return fmt.Errorf("posting reopen thread reply: %w", postErr)
		}
	}

	return nil
}

// buildIncidentReopenedThreadReply builds a thread reply for reopened incidents.
func (s *SlackSender) buildIncidentReopenedThreadReply(payload *Payload) *slack.MessageResponse {
	relapseCount := payload.Incident.RelapseCount
	text := fmt.Sprintf(
		":warning: Incident reopened (relapse #%d). Now requires %d consecutive successes to resolve.",
		relapseCount, payload.Check.RecoveryThreshold+relapseCount,
	)

	blocks := []slack.Block{
		{
			Type: slack.BlockTypeSection,
			Text: &slack.Text{
				Type: slack.BlockTypeMrkdwn,
				Text: text,
			},
		},
	}

	return &slack.MessageResponse{
		Text: text,
		Attachments: []slack.Attachment{
			{
				Color:    colorWarning,
				Fallback: text,
				Blocks:   blocks,
			},
		},
	}
}

// buildReopenedUpdateMessage builds the message to update the original incident message
// when the incident is reopened. It restores the message to active state.
func (s *SlackSender) buildReopenedUpdateMessage(payload *Payload) *slack.MessageResponse {
	checkName := getCheckName(payload.Check)
	fallbackText := fmt.Sprintf("Incident reopened for %s (relapse #%d)", checkName, payload.Incident.RelapseCount)

	fields := s.buildIncidentFields(payload, checkName)
	blocks := []slack.Block{
		{
			Type: slack.BlockTypeHeader,
			Text: &slack.Text{
				Type:  slack.BlockTypePlainText,
				Text:  fmt.Sprintf("Incident reopened for %s (relapse #%d)", checkName, payload.Incident.RelapseCount),
				Emoji: true,
			},
		},
		{Type: slack.BlockTypeSection, Fields: fields},
		{
			Type: slack.BlockTypeSection,
			Text: &slack.Text{Type: slack.BlockTypeMrkdwn, Text: "Please acknowledge the incident."},
		},
		s.buildIncidentActionButtons(payload.Incident.UID),
		{
			Type: slack.BlockTypeContext,
			Elements: []any{
				slack.ContextElement{
					Type: slack.BlockTypeMrkdwn,
					Text: ":warning: Reopened  :large_blue_circle: Monitor",
				},
			},
		},
		{
			Type: slack.BlockTypeContext,
			Elements: []any{
				slack.ContextElement{
					Type: slack.BlockTypeMrkdwn,
					Text: "Incident started " + formatTimestamp(payload.Incident.StartedAt),
				},
			},
		},
	}

	return &slack.MessageResponse{
		Text: fallbackText,
		Attachments: []slack.Attachment{
			{
				Color:    colorDanger,
				Fallback: fallbackText,
				Blocks:   blocks,
			},
		},
	}
}
