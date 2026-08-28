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

const mattermostTimeout = 30 * time.Second

// Mattermost attachment colors.
const (
	mattermostColorRed    = "#FF0000"
	mattermostColorGreen  = "#00FF00"
	mattermostColorOrange = "#FFA500"
	mattermostColorYellow = "#FFFF00"
	// Comments are conversation, not alarm — a neutral blue keeps them visually
	// distinct from every severity color above.
	mattermostColorBlue = "#3498DB"
)

// Mattermost field title labels.
const (
	mmFieldCheck        = "Check"
	mmFieldCause        = "Cause"
	mmFieldDuration     = "Duration"
	mmFieldFailureCount = "Failure Count"
)

var (
	// ErrMattermostWebhookURLNotConfigured is returned when the Mattermost webhook URL is missing.
	ErrMattermostWebhookURLNotConfigured = errors.New("mattermost webhook URL not configured")
	// errMattermostWebhookFailed is returned when the Mattermost webhook request fails.
	errMattermostWebhookFailed = errors.New("mattermost webhook failed")
)

// MattermostSender sends notifications via Mattermost incoming webhooks.
type MattermostSender struct{}

// Send sends a notification to Mattermost.
func (s *MattermostSender) Send(ctx context.Context, _ *jobdef.JobContext, payload *Payload) error {
	settings, err := s.parseSettings(payload)
	if err != nil {
		return err
	}

	msg := s.buildMessage(settings, payload)

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling mattermost payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, settings.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating mattermost request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", productName)

	client := newHTTPClient(mattermostTimeout)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sending mattermost webhook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)

		return fmt.Errorf("%w: status %d: %s", errMattermostWebhookFailed, resp.StatusCode, string(respBody))
	}

	return nil
}

type mattermostSettings struct {
	// WebhookURL uses snake_case to match the dashboard form field
	// (integration-form.tsx's UrlPanel writes "webhook_url") and Discord's
	// DiscordSettings, not the Mattermost incoming-webhook API's own casing.
	WebhookURL string `json:"webhook_url"` //nolint:tagliatelle // matches dashboard form key
	Channel    string `json:"channel"`
	Username   string `json:"username"`
	IconURL    string `json:"icon_url"` //nolint:tagliatelle // aligned with the outgoing Mattermost payload field
}

func (s *MattermostSender) parseSettings(payload *Payload) (*mattermostSettings, error) {
	data, err := json.Marshal(payload.Integration.Settings)
	if err != nil {
		return nil, fmt.Errorf("parsing mattermost settings: %w", err)
	}

	var settings mattermostSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parsing mattermost settings: %w", err)
	}

	// Backward compatibility: some rows may still carry the legacy camelCase
	// "webhookUrl" key (e.g. created via raw API calls before the struct tag
	// was aligned to "webhook_url" to match the dashboard form).
	settings.WebhookURL = webhookURLWithLegacyFallback(settings.WebhookURL, payload.Integration.Settings)

	if settings.WebhookURL == "" {
		return nil, ErrMattermostWebhookURLNotConfigured
	}

	if settings.Username == "" {
		settings.Username = productName
	}

	return &settings, nil
}

type mattermostMessage struct {
	Channel     string                 `json:"channel,omitempty"`
	Username    string                 `json:"username,omitempty"`
	IconURL     string                 `json:"icon_url,omitempty"` //nolint:tagliatelle // Mattermost API format
	Attachments []mattermostAttachment `json:"attachments"`
}

type mattermostAttachment struct {
	Fallback string            `json:"fallback"`
	Color    string            `json:"color"`
	Title    string            `json:"title"`
	Fields   []mattermostField `json:"fields"`
	Footer   string            `json:"footer"`
	Ts       int64             `json:"ts"`
}

type mattermostField struct {
	Short bool   `json:"short"`
	Title string `json:"title"`
	Value string `json:"value"`
}

func (s *MattermostSender) buildMessage(settings *mattermostSettings, payload *Payload) *mattermostMessage {
	checkName := getCheckName(payload.Check)
	color, title := s.eventColorAndTitle(payload, checkName)
	fields := s.buildFields(payload, checkName)
	fallback := title + " -- " + getFailureReason(payload.Incident)

	msg := &mattermostMessage{
		Username: settings.Username,
		Attachments: []mattermostAttachment{
			{
				Fallback: fallback,
				Color:    color,
				Title:    title,
				Fields:   fields,
				Footer:   productName,
				Ts:       payload.Incident.StartedAt.Unix(),
			},
		},
	}

	if settings.Channel != "" {
		msg.Channel = settings.Channel
	}

	if settings.IconURL != "" {
		msg.IconURL = settings.IconURL
	}

	return msg
}

func (s *MattermostSender) eventColorAndTitle(payload *Payload, checkName string) (string, string) {
	switch payload.EventType {
	case eventTypeIncidentCreated:
		return mattermostColorRed, ":red_circle: [DOWN] " + checkName
	case eventTypeIncidentResolved:
		return mattermostColorGreen, ":white_check_mark: [RECOVERED] " + checkName
	case eventTypeIncidentEscalated:
		return mattermostColorOrange, ":warning: [ESCALATED] " + checkName
	case eventTypeIncidentReopened:
		return mattermostColorYellow, ":repeat: [REOPENED] " + checkName
	case eventTypeIncidentComment:
		return mattermostColorBlue, ":speech_balloon: [COMMENT] " + checkName
	case eventTypeIncidentAcknowledged:
		return mattermostColorBlue, ":white_check_mark: [ACKNOWLEDGED] " + checkName
	case eventTypeIncidentUnacknowledged:
		return mattermostColorOrange, ":warning: [UNACKNOWLEDGED] " + checkName
	default:
		return mattermostColorOrange, "[UPDATE] " + checkName
	}
}

func (s *MattermostSender) buildFields(payload *Payload, checkName string) []mattermostField {
	if payload.EventType == eventTypeIncidentResolved {
		return s.buildResolvedFields(payload, checkName)
	}

	if payload.EventType == eventTypeIncidentComment {
		return []mattermostField{
			{Short: true, Title: mmFieldCheck, Value: checkName},
			{Short: true, Title: fieldLabelAuthor, Value: commentAuthor(payload.Comment)},
			{Short: false, Title: fieldLabelComment, Value: commentText(payload.Comment)},
		}
	}

	if payload.EventType == eventTypeIncidentAcknowledged ||
		payload.EventType == eventTypeIncidentUnacknowledged {
		label := fieldLabelAcknowledgedBy
		if payload.EventType == eventTypeIncidentUnacknowledged {
			label = fieldLabelUnacknowledgedBy
		}

		fields := []mattermostField{
			{Short: true, Title: mmFieldCheck, Value: checkName},
			{Short: true, Title: label, Value: ackActor(payload.Acknowledgment)},
		}

		if via := ackViaName(payload.Acknowledgment); via != "" {
			fields = append(fields, mattermostField{Short: true, Title: fieldLabelVia, Value: via})
		}

		if payload.EventType == eventTypeIncidentUnacknowledged {
			fields = append(fields, mattermostField{Short: false, Title: "Status", Value: unackCallToAction})
		}

		return fields
	}

	fields := []mattermostField{
		{Short: true, Title: mmFieldCheck, Value: checkName},
		{Short: true, Title: "Type", Value: payload.Check.Type},
		{Short: false, Title: mmFieldCause, Value: getFailureReason(payload.Incident)},
		{Short: true, Title: mmFieldFailureCount, Value: strconv.Itoa(payload.Incident.FailureCount)},
	}

	if payload.EventType == eventTypeIncidentReopened {
		fields = append(fields, mattermostField{
			Short: true,
			Title: "Relapse",
			Value: fmt.Sprintf("#%d", payload.Incident.RelapseCount),
		})
	}

	return fields
}

func (s *MattermostSender) buildResolvedFields(payload *Payload, checkName string) []mattermostField {
	fields := []mattermostField{
		{Short: true, Title: mmFieldCheck, Value: checkName},
	}

	if payload.Incident.ResolvedAt != nil {
		duration := payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt)
		fields = append(fields, mattermostField{
			Short: true,
			Title: mmFieldDuration,
			Value: formatDuration(duration),
		})
	}

	return fields
}
