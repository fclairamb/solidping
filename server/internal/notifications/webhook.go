package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

const webhookTimeout = 30 * time.Second

var (
	// ErrWebhookURLNotConfigured is returned when the webhook URL is not configured.
	ErrWebhookURLNotConfigured = errors.New("webhook url not configured")
	// ErrWebhookRequestFailed is returned when the webhook request returns a non-2xx status code.
	ErrWebhookRequestFailed = errors.New("webhook request failed")
)

// WebhookSender sends notifications via HTTP webhooks.
type WebhookSender struct{}

// Send sends a notification via webhook.
func (s *WebhookSender) Send(ctx context.Context, _ *jobdef.JobContext, payload *Payload) error {
	// Extract webhook URL from settings
	url, ok := payload.Connection.Settings["url"].(string)
	if !ok || url == "" {
		return ErrWebhookURLNotConfigured
	}

	// Build webhook payload
	webhookPayload := s.buildPayload(payload)

	body, err := json.Marshal(webhookPayload)
	if err != nil {
		return fmt.Errorf("marshaling webhook payload: %w", err)
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating webhook request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "SolidPing/1.0")

	// Add custom headers if configured
	if headers, ok := payload.Connection.Settings["headers"].(map[string]any); ok {
		for k, v := range headers {
			if str, ok := v.(string); ok {
				req.Header.Set(k, str)
			}
		}
	}

	// Send request
	client := &http.Client{Timeout: webhookTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sending webhook: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	// Check status
	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%w: status %d: %s", ErrWebhookRequestFailed, resp.StatusCode, string(respBody))
	}

	return nil
}

func (s *WebhookSender) buildPayload(payload *Payload) map[string]any {
	checkName := ""
	if payload.Check.Name != nil {
		checkName = *payload.Check.Name
	} else if payload.Check.Slug != nil {
		checkName = *payload.Check.Slug
	}

	result := map[string]any{
		"eventType":   payload.EventType,
		"incidentUid": payload.Incident.UID,
		"checkUid":    payload.Check.UID,
		"checkName":   checkName,
		"checkType":   payload.Check.Type,
		"startedAt":   payload.Incident.StartedAt,
	}

	if payload.Incident.ResolvedAt != nil {
		result["resolvedAt"] = payload.Incident.ResolvedAt
		result["durationSeconds"] = int(payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt).Seconds())
	}

	if payload.Incident.Title != nil {
		result["title"] = *payload.Incident.Title
	}

	result["failureCount"] = payload.Incident.FailureCount

	if payload.Incident.RelapseCount > 0 {
		result["relapseCount"] = payload.Incident.RelapseCount
		result["effectiveRecoveryThreshold"] = payload.Check.RecoveryThreshold + min(payload.Incident.RelapseCount, 5)
	}

	return result
}
