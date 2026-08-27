package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

const gotifyTimeout = 30 * time.Second

// gotifyDefaultPriority is used when the connection settings don't specify
// one. Gotify's own default priority for a message with none set is 0 (no
// notification), which is unhelpfully silent for a monitoring alert — 5
// matches ntfy's "default" tier instead.
const gotifyDefaultPriority = 5

// gotifyResolvedPriority is the priority sent for incident.resolved,
// regardless of the configured default: a recovery should never re-buzz a
// phone the way a page does.
const gotifyResolvedPriority = 2

var (
	// ErrGotifyServerURLNotConfigured is returned when the Gotify server URL is missing.
	ErrGotifyServerURLNotConfigured = errors.New("gotify server url not configured")
	// ErrGotifyAppTokenNotConfigured is returned when the Gotify application token is missing.
	ErrGotifyAppTokenNotConfigured = errors.New("gotify app token not configured")
	// errGotifyRequestFailed is returned when the Gotify request fails.
	errGotifyRequestFailed = errors.New("gotify request failed")
)

// GotifySender sends notifications to a self-hosted Gotify server.
type GotifySender struct{}

// gotifySettings is the per-connection settings shape for a Gotify channel.
// The keys use snake_case to match the dashboard form field names
// (integration-form.tsx writes "server_url" / "app_token"), not Go's default
// camelCase convention.
type gotifySettings struct {
	ServerURL string `json:"server_url"` //nolint:tagliatelle // matches dashboard form key
	AppToken  string `json:"app_token"`  //nolint:tagliatelle // matches dashboard form key
	Priority  *int   `json:"priority"`
}

// gotifyMessage is the JSON body Gotify's POST /message endpoint expects.
type gotifyMessage struct {
	Title    string         `json:"title"`
	Message  string         `json:"message"`
	Priority int            `json:"priority"`
	Extras   map[string]any `json:"extras,omitempty"`
}

// Send sends a notification to Gotify.
func (s *GotifySender) Send(ctx context.Context, _ *jobdef.JobContext, payload *Payload) error {
	settings, err := s.parseSettings(payload)
	if err != nil {
		return err
	}

	msg := s.buildMessage(settings, payload)

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling gotify message: %w", err)
	}

	url := strings.TrimRight(settings.ServerURL, "/") + "/message"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return fmt.Errorf("creating gotify request: %w", err)
	}

	// The token travels as a header, never as a "?token=" query parameter —
	// tokens don't belong in URLs (logs, proxies, browser history all leak
	// query strings).
	req.Header.Set("X-Gotify-Key", settings.AppToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", productName)

	client := newHTTPClient(gotifyTimeout)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sending gotify notification: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)

		return fmt.Errorf("%w: status %d: %s", errGotifyRequestFailed, resp.StatusCode, string(respBody))
	}

	return nil
}

func (s *GotifySender) parseSettings(payload *Payload) (*gotifySettings, error) {
	data, err := json.Marshal(payload.Integration.Settings)
	if err != nil {
		return nil, fmt.Errorf("parsing gotify settings: %w", err)
	}

	var settings gotifySettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parsing gotify settings: %w", err)
	}

	if settings.ServerURL == "" {
		return nil, ErrGotifyServerURLNotConfigured
	}

	if settings.AppToken == "" {
		return nil, ErrGotifyAppTokenNotConfigured
	}

	return &settings, nil
}

// getPriority returns the priority for eventType: the configured (or
// default) priority for everything except incident.resolved, which always
// sends gotifyResolvedPriority regardless of configuration.
func (s *GotifySender) getPriority(settings *gotifySettings, eventType string) int {
	if eventType == eventTypeIncidentResolved {
		return gotifyResolvedPriority
	}

	if settings.Priority != nil {
		return *settings.Priority
	}

	return gotifyDefaultPriority
}

func (s *GotifySender) buildMessage(settings *gotifySettings, payload *Payload) gotifyMessage {
	checkName := getCheckName(payload.Check)
	priority := s.getPriority(settings, payload.EventType)

	var title, body string

	switch payload.EventType {
	case eventTypeIncidentCreated:
		title = "[DOWN] " + checkName
		body = s.buildDownBody(payload, checkName)
	case eventTypeIncidentResolved:
		title = "[RECOVERED] " + checkName
		body = s.buildResolvedBody(payload, checkName)
	case eventTypeIncidentEscalated:
		title = "[ESCALATED] " + checkName
		body = s.buildEscalatedBody(payload, checkName)
	case eventTypeIncidentReopened:
		title = fmt.Sprintf("[REOPENED] %s (relapse #%d)", checkName, payload.Incident.RelapseCount)
		body = s.buildDownBody(payload, checkName)
	case eventTypeIncidentComment:
		title = commentTitle(payload)
		body = commentPlainBody(payload)
	case eventTypeIncidentAcknowledged:
		title = ackTitle(payload)
		body = ackPlainBody(payload)
	default:
		title = "[UPDATE] " + checkName
		body = "An incident update occurred for " + checkName
	}

	return gotifyMessage{
		Title:    title,
		Message:  body,
		Priority: priority,
		Extras:   s.buildExtras(payload),
	}
}

// buildExtras returns the Gotify "client::notification" extra that deep-links
// the notification to the incident, mirroring how the ntfy sender enriches
// its messages with the same incident URL. Nil (omitted) when there's no
// incident URL to link to.
func (s *GotifySender) buildExtras(payload *Payload) map[string]any {
	incidentURL := incidentDashURL(payload.AppBaseURL, payload.OrgSlug, payload.Incident)
	if incidentURL == "" {
		return nil
	}

	return map[string]any{
		"client::notification": map[string]any{
			"click": map[string]any{
				"url": incidentURL,
			},
		},
	}
}

func (s *GotifySender) buildDownBody(payload *Payload, checkName string) string {
	var builder strings.Builder

	fmt.Fprintf(&builder, "Check: %s (%s)\n", checkName, payload.Check.Type)
	fmt.Fprintf(&builder, "Cause: %s\n", getFailureReason(payload.Incident))
	fmt.Fprintf(&builder, "Failure count: %d\n", payload.Incident.FailureCount)
	fmt.Fprintf(&builder, "Started: %s", payload.Incident.StartedAt.Format(time.RFC3339))

	return builder.String()
}

func (s *GotifySender) buildResolvedBody(payload *Payload, checkName string) string {
	var builder strings.Builder

	fmt.Fprintf(&builder, "Check: %s (%s)\n", checkName, payload.Check.Type)

	if payload.Incident.ResolvedAt != nil {
		duration := payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt)
		fmt.Fprintf(&builder, "Duration: %s", formatDuration(duration))
	}

	return builder.String()
}

func (s *GotifySender) buildEscalatedBody(payload *Payload, checkName string) string {
	var builder strings.Builder

	fmt.Fprintf(&builder, "Check: %s (%s)\n", checkName, payload.Check.Type)
	fmt.Fprintf(&builder, "Cause: %s\n", getFailureReason(payload.Incident))
	fmt.Fprintf(&builder, "Failures: %d\n", payload.Incident.FailureCount)
	fmt.Fprintf(&builder, "Duration: %s", formatDuration(time.Since(payload.Incident.StartedAt)))

	return builder.String()
}
