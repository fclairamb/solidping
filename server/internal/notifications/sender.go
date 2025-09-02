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
	EventType               string                        // "incident.created", "incident.resolved", "incident.escalated"
	Incident                *models.Incident              // The incident
	Check                   *models.Check                 // The check
	Connection              *models.IntegrationConnection // The connection to send via
	CheckConnectionSettings *models.JSONMap               // Optional check-level override settings
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
