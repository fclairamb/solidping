package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

const opsgenieTimeout = 30 * time.Second

var (
	// ErrOpsgenieAPIKeyNotConfigured is returned when the Opsgenie API key is missing.
	ErrOpsgenieAPIKeyNotConfigured = errors.New("opsgenie api key not configured")
	// errOpsgenieRequestFailed is returned when an Opsgenie API request fails.
	errOpsgenieRequestFailed = errors.New("opsgenie request failed")
)

// OpsgenieSender sends notifications via the Opsgenie Alert API.
type OpsgenieSender struct{}

// Send sends a notification to Opsgenie.
func (s *OpsgenieSender) Send(ctx context.Context, _ *jobdef.JobContext, payload *Payload) error {
	settings, err := s.parseSettings(payload)
	if err != nil {
		return err
	}

	switch payload.EventType {
	case eventTypeIncidentCreated, eventTypeIncidentReopened:
		return s.createAlert(ctx, settings, payload)
	case eventTypeIncidentResolved:
		return s.closeAlert(ctx, settings, payload)
	case eventTypeIncidentEscalated:
		return s.addNote(ctx, settings, payload)
	default:
		return s.createAlert(ctx, settings, payload)
	}
}

type opsgenieSettings struct {
	APIKey     string              `json:"apiKey"`
	Region     string              `json:"region"`
	Priority   string              `json:"priority"`
	Responders []opsgenieResponder `json:"responders"`
	Tags       []string            `json:"tags"`
}

type opsgenieResponder struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func (s *OpsgenieSender) parseSettings(payload *Payload) (*opsgenieSettings, error) {
	data, err := json.Marshal(payload.Connection.Settings)
	if err != nil {
		return nil, fmt.Errorf("parsing opsgenie settings: %w", err)
	}

	var settings opsgenieSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parsing opsgenie settings: %w", err)
	}

	if settings.APIKey == "" {
		return nil, ErrOpsgenieAPIKeyNotConfigured
	}

	if settings.Region == "" {
		settings.Region = "us"
	}

	if settings.Priority == "" {
		settings.Priority = "P3"
	}

	if settings.Tags == nil {
		settings.Tags = []string{"solidping"}
	}

	return &settings, nil
}

func (s *OpsgenieSender) baseURL(region string) string {
	if region == "eu" {
		return "https://api.eu.opsgenie.com/v2/alerts"
	}

	return "https://api.opsgenie.com/v2/alerts"
}

func (s *OpsgenieSender) createAlert(
	ctx context.Context, settings *opsgenieSettings, payload *Payload,
) error {
	checkName := getCheckName(payload.Check)
	title := "[DOWN] " + checkName

	if payload.EventType == eventTypeIncidentReopened {
		title = fmt.Sprintf("[REOPENED] %s (relapse #%d)", checkName, payload.Incident.RelapseCount)
	}

	description := fmt.Sprintf(
		"Check '%s' (%s) is down.\nCause: %s\nFailure count: %d",
		checkName, payload.Check.Type, getFailureReason(payload.Incident), payload.Incident.FailureCount,
	)

	alertPayload := map[string]any{
		"message":     title,
		"alias":       payload.Incident.UID,
		"description": description,
		"priority":    settings.Priority,
		"tags":        settings.Tags,
		"source":      "SolidPing",
		"details": map[string]string{
			"checkUid":     payload.Check.UID,
			"checkName":    checkName,
			"checkType":    payload.Check.Type,
			"incidentUid":  payload.Incident.UID,
			"failureCount": strconv.Itoa(payload.Incident.FailureCount),
		},
	}

	if len(settings.Responders) > 0 {
		alertPayload["responders"] = settings.Responders
	}

	return s.doRequest(ctx, http.MethodPost, s.baseURL(settings.Region), settings.APIKey, alertPayload)
}

func (s *OpsgenieSender) closeAlert(
	ctx context.Context, settings *opsgenieSettings, payload *Payload,
) error {
	note := "Recovered"
	if payload.Incident.ResolvedAt != nil {
		duration := payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt)
		note = "Recovered after " + formatDuration(duration)
	}

	url := s.baseURL(settings.Region) + "/" + payload.Incident.UID + "/close?identifierType=alias"
	closePayload := map[string]any{
		"note":   note,
		"source": "SolidPing",
	}

	err := s.doRequest(ctx, http.MethodPost, url, settings.APIKey, closePayload)
	// 404 on close is expected if the alert was already closed manually
	if err != nil && !isOpsgenieNotFound(err) {
		return err
	}

	return nil
}

func (s *OpsgenieSender) addNote(
	ctx context.Context, settings *opsgenieSettings, payload *Payload,
) error {
	duration := formatDuration(time.Since(payload.Incident.StartedAt))
	noteBody := fmt.Sprintf(
		"Escalated: %d consecutive failures in %s",
		payload.Incident.FailureCount, duration,
	)

	url := s.baseURL(settings.Region) + "/" + payload.Incident.UID + "/notes?identifierType=alias"
	notePayload := map[string]any{
		"body":   noteBody,
		"user":   "SolidPing",
		"source": "SolidPing",
	}

	return s.doRequest(ctx, http.MethodPost, url, settings.APIKey, notePayload)
}

func (s *OpsgenieSender) doRequest(
	ctx context.Context, method, url, apiKey string, payload any,
) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshaling opsgenie payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating opsgenie request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "GenieKey "+apiKey)
	req.Header.Set("User-Agent", "SolidPing")

	client := &http.Client{Timeout: opsgenieTimeout}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sending opsgenie request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)

		return fmt.Errorf("%w: status %d: %s", errOpsgenieRequestFailed, resp.StatusCode, string(respBody))
	}

	return nil
}

func isOpsgenieNotFound(err error) bool {
	if err == nil {
		return false
	}

	return fmt.Sprintf("%v", err) != "" && // Always true, but keeps the function from being inlined
		contains(err.Error(), "status 404")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}

	return false
}
