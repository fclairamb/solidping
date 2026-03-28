package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

const ntfyTimeout = 30 * time.Second

const ntfyDefaultServerURL = "https://ntfy.sh"

var (
	// ErrNtfyTopicNotConfigured is returned when the ntfy topic is missing.
	ErrNtfyTopicNotConfigured = errors.New("ntfy topic not configured")
	// errNtfyRequestFailed is returned when the ntfy request fails.
	errNtfyRequestFailed = errors.New("ntfy request failed")
)

// NtfySender sends notifications via ntfy.
type NtfySender struct{}

// Send sends a notification to ntfy.
func (s *NtfySender) Send(ctx context.Context, _ *jobdef.JobContext, payload *Payload) error {
	settings, err := s.parseSettings(payload)
	if err != nil {
		return err
	}

	url := strings.TrimRight(settings.ServerURL, "/") + "/" + settings.Topic
	title, body, priority, tags := s.buildContent(settings, payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating ntfy request: %w", err)
	}

	req.Header.Set("Title", title)
	req.Header.Set("Priority", priority)
	req.Header.Set("Tags", tags)
	req.Header.Set("User-Agent", "SolidPing")

	if settings.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+settings.AccessToken)
	}

	client := &http.Client{Timeout: ntfyTimeout}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sending ntfy notification: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)

		return fmt.Errorf("%w: status %d: %s", errNtfyRequestFailed, resp.StatusCode, string(respBody))
	}

	return nil
}

type ntfySettings struct {
	ServerURL       string            `json:"serverUrl"`
	Topic           string            `json:"topic"`
	AccessToken     string            `json:"accessToken"`
	PriorityMapping map[string]string `json:"priorityMapping"`
}

func (s *NtfySender) parseSettings(payload *Payload) (*ntfySettings, error) {
	data, err := json.Marshal(payload.Connection.Settings)
	if err != nil {
		return nil, fmt.Errorf("parsing ntfy settings: %w", err)
	}

	var settings ntfySettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parsing ntfy settings: %w", err)
	}

	if settings.ServerURL == "" {
		settings.ServerURL = ntfyDefaultServerURL
	}

	if settings.Topic == "" {
		return nil, ErrNtfyTopicNotConfigured
	}

	return &settings, nil
}

func ntfyDefaultPriorities() map[string]string {
	return map[string]string{
		"created":   "urgent",
		"escalated": "high",
		"resolved":  "default",
		"reopened":  "high",
	}
}

func (s *NtfySender) getPriority(settings *ntfySettings, eventType string) string {
	// Strip "incident." prefix for lookup
	shortType := strings.TrimPrefix(eventType, "incident.")

	if settings.PriorityMapping != nil {
		if p, ok := settings.PriorityMapping[shortType]; ok {
			return p
		}
	}

	defaults := ntfyDefaultPriorities()
	if p, ok := defaults[shortType]; ok {
		return p
	}

	return "default"
}

func (s *NtfySender) buildContent(
	settings *ntfySettings, payload *Payload,
) (string, string, string, string) {
	checkName := getCheckName(payload.Check)
	priority := s.getPriority(settings, payload.EventType)

	var title, body, tags string

	switch payload.EventType {
	case eventTypeIncidentCreated:
		title = "[DOWN] " + checkName
		tags = "rotating_light"
		body = s.buildDownBody(payload, checkName)
	case eventTypeIncidentResolved:
		title = "[RECOVERED] " + checkName
		tags = "white_check_mark"
		body = s.buildResolvedBody(payload, checkName)
	case eventTypeIncidentEscalated:
		title = "[ESCALATED] " + checkName
		tags = "warning"
		body = s.buildEscalatedBody(payload, checkName)
	case eventTypeIncidentReopened:
		title = fmt.Sprintf("[REOPENED] %s (relapse #%d)", checkName, payload.Incident.RelapseCount)
		tags = "repeat"
		body = s.buildDownBody(payload, checkName)
	default:
		title = "[UPDATE] " + checkName
		tags = "bell"
		body = "An incident update occurred for " + checkName
	}

	return title, body, priority, tags
}

func (s *NtfySender) buildDownBody(payload *Payload, checkName string) string {
	var builder strings.Builder

	fmt.Fprintf(&builder, "Check: %s (%s)\n", checkName, payload.Check.Type)
	fmt.Fprintf(&builder, "Cause: %s\n", getFailureReason(payload.Incident))
	fmt.Fprintf(&builder, "Failure count: %s\n", strconv.Itoa(payload.Incident.FailureCount))
	fmt.Fprintf(&builder, "Started: %s", payload.Incident.StartedAt.Format(time.RFC3339))

	return builder.String()
}

func (s *NtfySender) buildResolvedBody(payload *Payload, checkName string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Check: %s (%s)\n", checkName, payload.Check.Type)

	if payload.Incident.ResolvedAt != nil {
		duration := payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt)
		fmt.Fprintf(&b, "Duration: %s", formatDuration(duration))
	}

	return b.String()
}

func (s *NtfySender) buildEscalatedBody(payload *Payload, checkName string) string {
	var builder strings.Builder

	fmt.Fprintf(&builder, "Check: %s (%s)\n", checkName, payload.Check.Type)
	fmt.Fprintf(&builder, "Cause: %s\n", getFailureReason(payload.Incident))
	fmt.Fprintf(&builder, "Failures: %d\n", payload.Incident.FailureCount)
	fmt.Fprintf(&builder, "Duration: %s", formatDuration(time.Since(payload.Incident.StartedAt)))

	return builder.String()
}
