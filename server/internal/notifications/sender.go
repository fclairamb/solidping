// Package notifications provides sender interfaces and implementations for
// sending incident notifications through various channels (email, Slack, webhooks).
package notifications

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// Payload contains all data needed to send a notification.
type Payload struct {
	EventType               string              // "incident.created", "incident.resolved", "incident.escalated"
	Incident                *models.Incident    // The incident
	Check                   *models.Check       // The check
	Integration             *models.Integration // The integration to send via
	CheckConnectionSettings *models.JSONMap     // Optional check-level override settings
	// OrgSlug is the organization slug used to build user-facing URLs (e.g.
	// magic-link ack URLs). Populated by the notification job runner.
	OrgSlug string
	// AppBaseURL is the application base URL used to build user-facing dashboard
	// links (e.g. Slack hyperlinks to the check and incident pages). Populated
	// by the notification job runner from jctx.AppConfig.Server.BaseURL.
	AppBaseURL string
	// MessageID is set by a sender after delivery to surface a provider-side
	// message identifier for the audit row (e.g. the Standard Webhooks
	// `webhook-id`). Empty for senders that have no such concept.
	MessageID string
	// DeliveryDetails is set by a sender after a delivery attempt (success or
	// failure) to surface structured artifacts — HTTP status code, the request
	// URL with secrets stripped, capped request/response bodies, duration — for
	// the audit row. Nil for senders/attempts that produce no artifacts.
	// Senders MUST never place secrets (signing secret, auth header) here.
	DeliveryDetails *models.DeliveryDetails
	// OnCallMentions names the humans the incident's escalation policy would
	// page first, so a channel message can tell them it is theirs. Populated by
	// the notification job runner only for `incident.created` and
	// `incident.escalated` on integrations whose settings enable it; always nil
	// otherwise, which is what makes "mentions off" and "resolved/reopened are
	// mention-free" true by construction rather than by sender discipline.
	OnCallMentions []MentionTarget
	// Comment carries the body and author of an `incident.comment` event.
	// Non-nil only for that event type; senders that render comments must
	// tolerate a nil here rather than assuming the fan-out filled it in.
	Comment *CommentInfo
	// Acknowledgment carries who took the incident and from where, for
	// `incident.acknowledged`. Non-nil only for that event type; senders must
	// tolerate a nil rather than assuming the fan-out filled it in.
	Acknowledgment *AckInfo
}

// MentionTarget is one human an alert should ping. ExternalID is the
// provider-side id (a Slack user id); it is empty when the person has no
// identity mapped on this integration, in which case the sender renders their
// plain-text name and nobody gets pinged.
type MentionTarget struct {
	UserUID     string
	DisplayName string
	ExternalID  string
}

// Sender is the interface for sending notifications via different channels.
type Sender interface {
	// Send sends a notification for the given payload.
	// Returns nil on success, error on failure.
	// Should return a retryable error for transient failures.
	Send(ctx context.Context, jctx *jobdef.JobContext, payload *Payload) error
}

// legacyWebhookURLSettingsKey is the camelCase settings key some pre-existing
// Google Chat / Mattermost integration rows still use (e.g. created via raw
// API calls following outdated docs, or from before the struct tags were
// aligned to "webhook_url" to match the dashboard form and Discord).
const legacyWebhookURLSettingsKey = "webhookUrl"

// webhookURLWithLegacyFallback returns url if non-empty, otherwise falls back
// to reading the legacy camelCase "webhookUrl" key out of the raw settings
// map. Shared by GoogleChatSender and MattermostSender so both senders keep
// accepting rows written before the "webhook_url" key alignment, without a
// data migration.
func webhookURLWithLegacyFallback(url string, settings models.JSONMap) string {
	if url != "" {
		return url
	}

	if v, ok := settings[legacyWebhookURLSettingsKey].(string); ok {
		return v
	}

	return ""
}

// IsNetworkError checks if an error is network-related (retryable).
func IsNetworkError(err error) bool {
	if err == nil {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	errStr := strings.ToLower(err.Error())
	networkIndicators := []string{
		"connection refused",
		"connection reset",
		"connection timed out",
		"no such host",
		"network is unreachable",
		"i/o timeout",
		"dial tcp",
		"dial failed",
	}

	for _, indicator := range networkIndicators {
		if strings.Contains(errStr, indicator) {
			return true
		}
	}

	return false
}
