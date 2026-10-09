package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/httpclientpool"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

const pushoverTimeout = 30 * time.Second

const pushoverAPIURL = "https://api.pushover.net/1/messages.json"

var (
	// ErrPushoverAPITokenNotConfigured is returned when the Pushover API token is missing.
	ErrPushoverAPITokenNotConfigured = newConfigError("pushover api token not configured", "API token")
	// ErrPushoverUserKeyNotConfigured is returned when the Pushover user key is missing.
	ErrPushoverUserKeyNotConfigured = newConfigError("pushover user key not configured", "user key")
	// errPushoverRequestFailed is returned when a Pushover API request fails.
	errPushoverRequestFailed = errors.New("pushover request failed")
	// errPushoverError is returned when Pushover returns an application-level error.
	errPushoverError = errors.New("pushover error")
)

// PushoverSender sends notifications via Pushover.
type PushoverSender struct {
	// APIURL overrides the Pushover messages endpoint for tests. Empty = the
	// real pushoverAPIURL. Mirrors PagerDutySender.EventsURL.
	APIURL string
}

// apiURL returns APIURL when set (tests), otherwise the real endpoint.
func (s *PushoverSender) apiURL() string {
	if s.APIURL != "" {
		return s.APIURL
	}

	return pushoverAPIURL
}

// Send sends a notification to Pushover.
func (s *PushoverSender) Send(ctx context.Context, _ *jobdef.JobContext, payload *Payload) error {
	settings, err := s.parseSettings(payload)
	if err != nil {
		return err
	}

	title, body, priority, sound := s.buildContent(settings, payload)

	data := url.Values{
		"token":    {settings.APIToken},
		"user":     {settings.UserKey},
		"title":    {title},
		"message":  {body},
		"priority": {strconv.Itoa(priority)},
		"sound":    {sound},
		"html":     {"1"},
	}

	if settings.Device != "" {
		data.Set("device", settings.Device)
	}

	// Emergency priority requires retry and expire params
	if priority == 2 {
		data.Set("retry", "300")
		data.Set("expire", "3600")
	}

	return s.doRequest(ctx, data)
}

func (s *PushoverSender) doRequest(ctx context.Context, data url.Values) error {
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, s.apiURL(), strings.NewReader(data.Encode()),
	)
	if err != nil {
		return fmt.Errorf("creating pushover request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", productName)

	client := httpclientpool.NewClient(pushoverTimeout)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sending pushover notification: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)

	// Pushover can return errors even with HTTP 200
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%w: status %d: %s", errPushoverRequestFailed, resp.StatusCode, string(respBody))
	}

	var result pushoverResponse
	if err := json.Unmarshal(respBody, &result); err == nil && result.Status == 0 {
		return fmt.Errorf("%w: %s", errPushoverError, strings.Join(result.Errors, "; "))
	}

	return nil
}

type pushoverResponse struct {
	Status int      `json:"status"`
	Errors []string `json:"errors"`
}

// Pushover setting keys. user_key / api_token are the canonical pair: the
// dashboard form writes them and the credentials registry encrypts them
// (crypto/credentials/conn_secrets.go). Spec 2026-10-08-03.
const (
	PushoverSettingUserKey  = "user_key"
	PushoverSettingAPIToken = "api_token"
)

// PushoverLegacyUserKeys and PushoverLegacyAPITokenKeys are the names older
// rows used for the same two settings: `user` / `token` (the dashboard form
// before spec 2026-10-08-03) and `userKey` / `apiToken` (the sender before it).
//
//nolint:gochecknoglobals // constant lookup tables
var (
	PushoverLegacyUserKeys     = []string{"user", "userKey"}
	PushoverLegacyAPITokenKeys = []string{"token", "apiToken"}
)

type pushoverSettings struct {
	APIToken        string         `json:"api_token"` //nolint:tagliatelle // form key and secret registry
	UserKey         string         `json:"user_key"`  //nolint:tagliatelle // form key and secret registry
	Device          string         `json:"device"`
	SoundDown       string         `json:"soundDown"`
	SoundUp         string         `json:"soundUp"`
	PriorityMapping map[string]int `json:"priorityMapping"`
}

func (s *PushoverSender) parseSettings(payload *Payload) (*pushoverSettings, error) {
	data, err := json.Marshal(payload.Integration.Settings)
	if err != nil {
		return nil, fmt.Errorf("parsing pushover settings: %w", err)
	}

	var settings pushoverSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parsing pushover settings: %w", err)
	}

	// TODO(2026-10-08-03): drop this fallback once every deployment has run
	// the startup backfill (credmigrate.NormalizePushoverSettings), which
	// rewrites legacy rows to the canonical keys.
	if settings.APIToken == "" {
		settings.APIToken = firstStringSetting(payload.Integration.Settings, PushoverLegacyAPITokenKeys)
	}

	if settings.UserKey == "" {
		settings.UserKey = firstStringSetting(payload.Integration.Settings, PushoverLegacyUserKeys)
	}

	if settings.APIToken == "" {
		return nil, ErrPushoverAPITokenNotConfigured
	}

	if settings.UserKey == "" {
		return nil, ErrPushoverUserKeyNotConfigured
	}

	if settings.SoundDown == "" {
		settings.SoundDown = "siren"
	}

	if settings.SoundUp == "" {
		settings.SoundUp = "magic"
	}

	return &settings, nil
}

func pushoverDefaultPriorities() map[string]int {
	return map[string]int{
		"created":   1,
		"escalated": 2,
		"resolved":  -1,
		"reopened":  1,
	}
}

func (s *PushoverSender) getPriority(settings *pushoverSettings, eventType string) int {
	shortType := strings.TrimPrefix(eventType, "incident.")

	if settings.PriorityMapping != nil {
		if p, ok := settings.PriorityMapping[shortType]; ok {
			return p
		}
	}

	defaults := pushoverDefaultPriorities()
	if p, ok := defaults[shortType]; ok {
		return p
	}

	return 0
}

func (s *PushoverSender) getSound(settings *pushoverSettings, eventType string) string {
	if eventType == eventTypeIncidentResolved {
		return settings.SoundUp
	}

	return settings.SoundDown
}

func (s *PushoverSender) buildContent(
	settings *pushoverSettings, payload *Payload,
) (string, string, int, string) {
	checkName := getCheckName(payload.Check)
	priority := s.getPriority(settings, payload.EventType)
	sound := s.getSound(settings, payload.EventType)

	var title, body string

	switch payload.EventType {
	case eventTypeIncidentCreated:
		title = "[DOWN] " + checkName
		body = s.buildDownBody(payload, checkName)
	case eventTypeIncidentResolved:
		title = resolvedTag(payload.Incident) + checkName
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
	case eventTypeIncidentUnacknowledged:
		title = unackTitle(payload)
		body = unackPlainBody(payload)
	default:
		title = "[UPDATE] " + checkName
		body = "An incident update occurred for " + checkName
	}

	return title, body, priority, sound
}

func (s *PushoverSender) buildDownBody(payload *Payload, checkName string) string {
	var builder strings.Builder

	fmt.Fprintf(&builder, "<b>Check:</b> %s (%s)\n", checkName, payload.Check.Type)
	fmt.Fprintf(&builder, "<b>Cause:</b> %s\n", getFailureReason(payload.Incident))
	fmt.Fprintf(&builder, "<b>Failures:</b> %d\n", payload.Incident.FailureCount)
	fmt.Fprintf(&builder, "<b>Started:</b> %s", payload.Incident.StartedAt.Format(time.RFC3339))

	return builder.String()
}

func (s *PushoverSender) buildResolvedBody(payload *Payload, checkName string) string {
	var builder strings.Builder

	if ResolvedByCheckDeletion(payload.Incident) {
		builder.WriteString(checkDeletedSentence + "\n")
	}

	fmt.Fprintf(&builder, "<b>Check:</b> %s (%s)\n", checkName, payload.Check.Type)

	if payload.Incident.ResolvedAt != nil {
		duration := payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt)
		fmt.Fprintf(&builder, "<b>Duration:</b> %s", formatDuration(duration))
	}

	return builder.String()
}

func (s *PushoverSender) buildEscalatedBody(payload *Payload, checkName string) string {
	var builder strings.Builder

	fmt.Fprintf(&builder, "<b>Check:</b> %s (%s)\n", checkName, payload.Check.Type)
	fmt.Fprintf(&builder, "<b>Cause:</b> %s\n", getFailureReason(payload.Incident))
	fmt.Fprintf(&builder, "<b>Failures:</b> %d\n", payload.Incident.FailureCount)
	fmt.Fprintf(&builder, "<b>Duration:</b> %s", formatDuration(time.Since(payload.Incident.StartedAt)))

	return builder.String()
}

// firstStringSetting returns the first non-empty string value among keys.
func firstStringSetting(settings map[string]any, keys []string) string {
	for _, key := range keys {
		if v, ok := settings[key].(string); ok && v != "" {
			return v
		}
	}

	return ""
}
