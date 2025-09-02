package notifications

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/discord"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// ErrDiscordWebhookURLNotConfigured is returned when the Discord webhook URL is missing.
var ErrDiscordWebhookURLNotConfigured = errors.New("discord webhook URL not configured")

// DiscordSender sends notifications via Discord webhooks.
type DiscordSender struct{}

// Send sends a notification to Discord.
func (ds *DiscordSender) Send(ctx context.Context, _ *jobdef.JobContext, payload *Payload) error {
	settings, err := ds.parseSettings(payload)
	if err != nil {
		return err
	}

	client := discord.NewClient(settings.WebhookURL)
	msg := ds.buildMessage(payload)

	if err := client.SendWebhookMessage(ctx, msg); err != nil {
		return fmt.Errorf("sending discord webhook message: %w", err)
	}

	return nil
}

// parseSettings extracts and validates Discord settings from the payload.
func (ds *DiscordSender) parseSettings(payload *Payload) (*models.DiscordSettings, error) {
	settings, err := models.DiscordSettingsFromJSONMap(payload.Connection.Settings)
	if err != nil {
		return nil, fmt.Errorf("parsing discord settings: %w", err)
	}

	if settings.WebhookURL == "" {
		return nil, ErrDiscordWebhookURLNotConfigured
	}

	return settings, nil
}

// buildMessage builds a Discord webhook message based on the event type.
func (ds *DiscordSender) buildMessage(payload *Payload) *discord.WebhookMessage {
	var embed discord.Embed

	switch payload.EventType {
	case eventTypeIncidentCreated:
		embed = ds.buildIncidentCreatedEmbed(payload)
	case eventTypeIncidentResolved:
		embed = ds.buildIncidentResolvedEmbed(payload)
	case eventTypeIncidentEscalated:
		embed = ds.buildIncidentEscalatedEmbed(payload)
	case eventTypeIncidentReopened:
		embed = ds.buildIncidentReopenedEmbed(payload)
	default:
		embed = ds.buildDefaultEmbed(payload)
	}

	return &discord.WebhookMessage{
		Username: "SolidPing",
		Embeds:   []discord.Embed{embed},
	}
}

// buildIncidentCreatedEmbed builds an embed for incident.created events.
func (ds *DiscordSender) buildIncidentCreatedEmbed(payload *Payload) discord.Embed {
	checkName := getCheckName(payload.Check)
	fields := ds.buildCommonFields(payload, checkName)

	return discord.Embed{
		Title:       "New incident for " + checkName,
		Description: "A new incident has been detected. Please investigate.",
		Color:       discord.ColorRed,
		Fields:      fields,
		Timestamp:   payload.Incident.StartedAt.Format(time.RFC3339),
		Footer:      &discord.Footer{Text: "SolidPing Monitoring"},
	}
}

// buildIncidentResolvedEmbed builds an embed for incident.resolved events.
func (ds *DiscordSender) buildIncidentResolvedEmbed(payload *Payload) discord.Embed {
	checkName := getCheckName(payload.Check)

	fields := []discord.Field{
		{Name: "Monitor", Value: checkName, Inline: true},
	}

	if payload.Incident.ResolvedAt != nil {
		duration := payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt)
		fields = append(fields, discord.Field{
			Name:   "Duration",
			Value:  formatDuration(duration),
			Inline: true,
		})
	}

	return discord.Embed{
		Title:       "Incident resolved for " + checkName,
		Description: "The incident has been automatically resolved.",
		Color:       discord.ColorGreen,
		Fields:      fields,
		Timestamp:   time.Now().Format(time.RFC3339),
		Footer:      &discord.Footer{Text: "SolidPing Monitoring"},
	}
}

// buildIncidentEscalatedEmbed builds an embed for incident.escalated events.
func (ds *DiscordSender) buildIncidentEscalatedEmbed(payload *Payload) discord.Embed {
	checkName := getCheckName(payload.Check)
	duration := formatDuration(time.Since(payload.Incident.StartedAt))

	fields := []discord.Field{
		{Name: "Monitor", Value: checkName, Inline: true},
		{Name: "Failures", Value: strconv.Itoa(payload.Incident.FailureCount), Inline: true},
		{Name: "Duration", Value: duration, Inline: true},
	}

	if url := getCheckURL(payload.Check); url != "" {
		method := getCheckMethod(payload.Check)
		fields = append(fields, discord.Field{
			Name:  "Check",
			Value: fmt.Sprintf("%s `%s`", method, url),
		})
	}

	return discord.Embed{
		Title:       "Incident escalated: " + checkName,
		Description: "This incident has exceeded the escalation threshold.",
		Color:       discord.ColorOrange,
		Fields:      fields,
		Timestamp:   time.Now().Format(time.RFC3339),
		Footer:      &discord.Footer{Text: "SolidPing Monitoring"},
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
			"Incident reopened for %s (relapse #%d)",
			checkName, payload.Incident.RelapseCount,
		),
		Description: fmt.Sprintf(
			"Now requires %d consecutive successes to resolve.",
			payload.Check.RecoveryThreshold+payload.Incident.RelapseCount,
		),
		Color:     discord.ColorRed,
		Fields:    fields,
		Timestamp: time.Now().Format(time.RFC3339),
		Footer:    &discord.Footer{Text: "SolidPing Monitoring"},
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
		Footer:      &discord.Footer{Text: "SolidPing Monitoring"},
	}
}

// buildCommonFields builds fields common to created and reopened embeds.
func (ds *DiscordSender) buildCommonFields(
	payload *Payload, checkName string,
) []discord.Field {
	fields := []discord.Field{
		{Name: "Monitor", Value: checkName, Inline: true},
		{Name: "Cause", Value: getFailureReason(payload.Incident), Inline: true},
	}

	if url := getCheckURL(payload.Check); url != "" {
		method := getCheckMethod(payload.Check)
		fields = append(fields, discord.Field{
			Name:  "Check",
			Value: fmt.Sprintf("%s `%s`", method, url),
		})
	}

	return fields
}
