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

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

const (
	pagerdutyTimeout   = 30 * time.Second
	pagerdutyEventsURL = "https://events.pagerduty.com/v2/enqueue"

	// PagerDuty Events API v2 severities. Every trigger event must carry
	// exactly one of these four values.
	pagerdutySeverityCritical = "critical"
	pagerdutySeverityError    = "error"
	pagerdutySeverityWarning  = "warning"
	pagerdutySeverityInfo     = "info"

	pagerdutyEventActionTrigger = "trigger"
	pagerdutyEventActionResolve = "resolve"

	// pagerdutyLinkText labels the incident-page link sent in every trigger.
	pagerdutyLinkText = "View incident in SolidPing"

	// pagerdutyFieldRoutingKey is the Events API v2 request's top-level
	// routing_key field name (distinct from the pagerdutySettings.RoutingKey
	// Go field it carries — same JSON key as the connection's own secret
	// settings field, "routing_key").
	pagerdutyFieldRoutingKey = "routing_key"

	// ResultStatus string forms (models.StatusToString) recorded on an
	// incident's failure snapshot, used to derive a PagerDuty severity.
	resultStatusStrDown     = "DOWN"
	resultStatusStrTimeout  = "TIMEOUT"
	resultStatusStrError    = "ERROR"
	resultStatusStrWarning  = "WARNING"
	resultStatusStrDegraded = "DEGRADED"
)

var (
	// ErrPagerdutyRoutingKeyNotConfigured is returned when the PagerDuty
	// Events API v2 routing (integration) key is missing.
	ErrPagerdutyRoutingKeyNotConfigured = errors.New("pagerduty routing key not configured")
	// ErrPagerdutyServerError flags a 429/5xx response from the Events API,
	// always wrapped as retryable.
	ErrPagerdutyServerError = errors.New("pagerduty server error")
	// errPagerdutyRequestFailed is returned for a non-2xx, non-retryable
	// response (e.g. a malformed event rejected with 400).
	errPagerdutyRequestFailed = errors.New("pagerduty request failed")
)

// PagerDutySender sends notifications via the PagerDuty Events API v2 ONLY
// (`POST https://events.pagerduty.com/v2/enqueue`) — no OAuth, no REST API
// v2, no schedule import. See spec 2026-08-19-02.
type PagerDutySender struct {
	// EventsURL overrides the Events API v2 endpoint for tests. Empty = the
	// real pagerdutyEventsURL. Mirrors TwilioSender.BaseURL.
	EventsURL string
}

// eventsURL returns EventsURL when set (tests), otherwise the real
// PagerDuty Events API v2 endpoint.
func (s *PagerDutySender) eventsURL() string {
	if s.EventsURL != "" {
		return s.EventsURL
	}

	return pagerdutyEventsURL
}

// Send sends a notification to PagerDuty.
func (s *PagerDutySender) Send(ctx context.Context, _ *jobdef.JobContext, payload *Payload) error {
	settings, err := s.parseSettings(payload)
	if err != nil {
		return err
	}

	switch payload.EventType {
	case eventTypeIncidentCreated, eventTypeIncidentReopened:
		return s.trigger(ctx, settings, payload)
	case eventTypeIncidentResolved:
		return s.resolve(ctx, settings, payload)
	case eventTypeIncidentEscalated, eventTypeIncidentComment:
		// Events API v2 has no note/annotation concept, and a `trigger` call
		// reusing an already-resolved incident's dedup_key would RE-OPEN it.
		// A comment or an escalation must therefore send NOTHING at all — not
		// even a best-effort annotation, since this API simply cannot express
		// one. This drop is deliberate and load-bearing: see spec 2026-08-19-02.
		return nil
	default:
		return s.trigger(ctx, settings, payload)
	}
}

type pagerdutySettings struct {
	RoutingKey string `json:"routing_key"` //nolint:tagliatelle // matches the settings JSONB / dashboard form key
}

func (s *PagerDutySender) parseSettings(payload *Payload) (*pagerdutySettings, error) {
	data, err := json.Marshal(payload.Integration.Settings)
	if err != nil {
		return nil, fmt.Errorf("parsing pagerduty settings: %w", err)
	}

	var settings pagerdutySettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parsing pagerduty settings: %w", err)
	}

	if settings.RoutingKey == "" {
		return nil, ErrPagerdutyRoutingKeyNotConfigured
	}

	return &settings, nil
}

func (s *PagerDutySender) trigger(ctx context.Context, settings *pagerdutySettings, payload *Payload) error {
	checkName := getCheckName(payload.Check)
	summary := "[DOWN] " + checkName

	if payload.EventType == eventTypeIncidentReopened {
		summary = fmt.Sprintf("[REOPENED] %s (relapse #%d)", checkName, payload.Incident.RelapseCount)
	}

	event := map[string]any{
		pagerdutyFieldRoutingKey: settings.RoutingKey,
		"event_action":           pagerdutyEventActionTrigger,
		// dedup_key = incident UID, so trigger/resolve (and any future
		// trigger for the same incident) all correlate to ONE PagerDuty
		// incident across its whole lifecycle.
		"dedup_key": payload.Incident.UID,
		"client":    productName,
		"payload": map[string]any{
			"summary":  summary,
			"source":   pagerdutySource(payload),
			"severity": pagerdutySeverity(payload.Incident),
			"custom_details": map[string]any{
				"checkUid":     payload.Check.UID,
				"checkName":    checkName,
				"checkType":    payload.Check.Type,
				"incidentUid":  payload.Incident.UID,
				"cause":        getFailureReason(payload.Incident),
				"failureCount": payload.Incident.FailureCount,
			},
		},
	}

	if url := checkDashURL(payload.AppBaseURL, payload.OrgSlug, payload.Check); url != "" {
		event["client_url"] = url
	}

	if links := pagerdutyLinks(payload); len(links) > 0 {
		event["links"] = links
	}

	return s.doRequest(ctx, event)
}

func (s *PagerDutySender) resolve(ctx context.Context, settings *pagerdutySettings, payload *Payload) error {
	event := map[string]any{
		pagerdutyFieldRoutingKey: settings.RoutingKey,
		"event_action":           pagerdutyEventActionResolve,
		"dedup_key":              payload.Incident.UID,
		"client":                 productName,
	}

	return s.doRequest(ctx, event)
}

// pagerdutySource returns the check's URL when it has one (HTTP-family
// checks), otherwise its name — PagerDuty's `payload.source` names "the
// specific system, application or service responsible for the event".
func pagerdutySource(payload *Payload) string {
	if url := getCheckURL(payload.Check); url != "" {
		return url
	}

	return getCheckName(payload.Check)
}

// pagerdutyLinks returns the single link back to the incident's dash0 page,
// or nil when no dashboard URL can be built (AppBaseURL/OrgSlug unset).
func pagerdutyLinks(payload *Payload) []map[string]string {
	url := incidentDashURL(payload.AppBaseURL, payload.OrgSlug, payload.Incident)
	if url == "" {
		return nil
	}

	return []map[string]string{{"href": url, "text": pagerdutyLinkText}}
}

// pagerdutySeverity maps the ResultStatus recorded on the incident's
// failure snapshot onto PagerDuty's four-value severity enum. SolidPing has
// no per-incident severity field of its own (the "severity" primitive is a
// per-org named channel-set, not an incident attribute), so this derives the
// closest equivalent from the result that triggered or is currently failing
// the incident.
func pagerdutySeverity(incident *models.Incident) string {
	switch latestFailureStatus(incident) {
	case resultStatusStrDown:
		return pagerdutySeverityCritical
	case resultStatusStrTimeout, resultStatusStrError:
		return pagerdutySeverityError
	case resultStatusStrWarning, resultStatusStrDegraded:
		return pagerdutySeverityWarning
	default:
		// Includes a missing/unrecognized snapshot: favor the least alarming
		// enum value rather than guessing at critical.
		return pagerdutySeverityInfo
	}
}

// latestFailureStatus reads the status string (StatusToString form, e.g.
// "DOWN"/"TIMEOUT") off the incident's most recent failure snapshot,
// preferring `last_failure` (kept current across a relapse) and falling back
// to `first_result` (written once, at open). Returns "" when neither
// snapshot carries a usable status — an incident predating the snapshot
// feature, for instance.
func latestFailureStatus(incident *models.Incident) string {
	if incident == nil || incident.Details == nil {
		return ""
	}

	for _, key := range []string{"last_failure", "first_result"} {
		snapshot, ok := incident.Details[key].(map[string]any)
		if !ok {
			continue
		}

		if status, ok := snapshot["status"].(string); ok && status != "" {
			return status
		}
	}

	return ""
}

func (s *PagerDutySender) doRequest(ctx context.Context, event map[string]any) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshaling pagerduty payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.eventsURL(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating pagerduty request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", productName)

	client := newHTTPClient(pagerdutyTimeout)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sending pagerduty request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)

	return classifyPagerdutyResponse(resp.StatusCode, respBody)
}

// classifyPagerdutyResponse turns an Events API v2 HTTP status into nil (2xx
// accepted), a retryable error (429 rate-limited, or 5xx transient), or a
// permanent error (anything else — e.g. a malformed event rejected with 400).
func classifyPagerdutyResponse(statusCode int, body []byte) error {
	if statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices {
		return nil
	}

	if statusCode == http.StatusTooManyRequests || statusCode >= http.StatusInternalServerError {
		return jobdef.NewRetryableError(
			fmt.Errorf("%w: status %d: %s", ErrPagerdutyServerError, statusCode, string(body)),
		)
	}

	return fmt.Errorf("%w: status %d: %s", errPagerdutyRequestFailed, statusCode, string(body))
}
