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
}

// Sender is the interface for sending notifications via different channels.
type Sender interface {
	// Send sends a notification for the given payload.
	// Returns nil on success, error on failure.
	// Should return a retryable error for transient failures.
	Send(ctx context.Context, jctx *jobdef.JobContext, payload *Payload) error
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
