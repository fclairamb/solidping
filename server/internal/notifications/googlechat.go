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

const googleChatTimeout = 30 * time.Second

var (
	// ErrGoogleChatWebhookURLNotConfigured is returned when the Google Chat webhook URL is missing.
	ErrGoogleChatWebhookURLNotConfigured = errors.New("google chat webhook URL not configured")
	// errGoogleChatWebhookFailed is returned when the Google Chat webhook request fails.
	errGoogleChatWebhookFailed = errors.New("google chat webhook failed")
)

// GoogleChatSender sends notifications via Google Chat webhooks.
type GoogleChatSender struct{}

// Send sends a notification to Google Chat.
func (s *GoogleChatSender) Send(ctx context.Context, _ *jobdef.JobContext, payload *Payload) error {
	settings, err := s.parseSettings(payload)
	if err != nil {
		return err
	}

	url := s.buildURL(settings, payload.Incident.UID)
	msg := s.buildMessage(payload)

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling google chat payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating google chat request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "SolidPing")

	client := &http.Client{Timeout: googleChatTimeout}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sending google chat webhook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)

		return fmt.Errorf("%w: status %d: %s", errGoogleChatWebhookFailed, resp.StatusCode, string(respBody))
	}

	return nil
}

type googleChatSettings struct {
	WebhookURL       string `json:"webhookUrl"`
	ThreadKeyEnabled bool   `json:"threadKeyEnabled"`
}

func (s *GoogleChatSender) parseSettings(payload *Payload) (*googleChatSettings, error) {
	data, err := json.Marshal(payload.Connection.Settings)
	if err != nil {
		return nil, fmt.Errorf("parsing google chat settings: %w", err)
	}

	var settings googleChatSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("parsing google chat settings: %w", err)
	}

	// Default threadKeyEnabled to true
	if _, ok := payload.Connection.Settings["threadKeyEnabled"]; !ok {
		settings.ThreadKeyEnabled = true
	}

	if settings.WebhookURL == "" {
		return nil, ErrGoogleChatWebhookURLNotConfigured
	}

	return &settings, nil
}

func (s *GoogleChatSender) buildURL(settings *googleChatSettings, incidentUID string) string {
	u := settings.WebhookURL
	if settings.ThreadKeyEnabled {
		u += "&threadKey=" + incidentUID +
			"&messageReplyOption=REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD"
	}

	return u
}

type googleChatMessage struct {
	CardsV2 []googleChatCardV2 `json:"cardsV2"`
}

type googleChatCardV2 struct {
	CardID string         `json:"cardId"`
	Card   googleChatCard `json:"card"`
}

type googleChatCard struct {
	Header   googleChatHeader    `json:"header"`
	Sections []googleChatSection `json:"sections"`
}

type googleChatHeader struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
}

type googleChatSection struct {
	Widgets []googleChatWidget `json:"widgets"`
}

type googleChatWidget struct {
	DecoratedText *googleChatDecoratedText `json:"decoratedText,omitempty"`
}

type googleChatDecoratedText struct {
	TopLabel string `json:"topLabel"`
	Text     string `json:"text"`
}

func (s *GoogleChatSender) buildMessage(payload *Payload) *googleChatMessage {
	checkName := getCheckName(payload.Check)

	var title, subtitle string

	switch payload.EventType {
	case eventTypeIncidentCreated:
		title = "[DOWN] " + checkName
		subtitle = getFailureReason(payload.Incident)
	case eventTypeIncidentResolved:
		title = "[RECOVERED] " + checkName
		subtitle = s.resolvedSubtitle(payload)
	case eventTypeIncidentEscalated:
		title = "[ESCALATED] " + checkName
		subtitle = fmt.Sprintf("%d consecutive failures", payload.Incident.FailureCount)
	case eventTypeIncidentReopened:
		title = fmt.Sprintf("[REOPENED] %s (relapse #%d)", checkName, payload.Incident.RelapseCount)
		subtitle = getFailureReason(payload.Incident)
	default:
		title = "[UPDATE] " + checkName
		subtitle = "An incident update occurred"
	}

	widgets := s.buildWidgets(payload, checkName)

	return &googleChatMessage{
		CardsV2: []googleChatCardV2{
			{
				CardID: "incident-" + payload.Incident.UID,
				Card: googleChatCard{
					Header:   googleChatHeader{Title: title, Subtitle: subtitle},
					Sections: []googleChatSection{{Widgets: widgets}},
				},
			},
		},
	}
}

func (s *GoogleChatSender) buildWidgets(payload *Payload, checkName string) []googleChatWidget {
	widgets := []googleChatWidget{
		{DecoratedText: &googleChatDecoratedText{TopLabel: "Check", Text: checkName + " (" + string(payload.Check.Type) + ")"}},
	}

	switch payload.EventType {
	case eventTypeIncidentResolved:
		if payload.Incident.ResolvedAt != nil {
			duration := payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt)
			widgets = append(widgets, googleChatWidget{
				DecoratedText: &googleChatDecoratedText{TopLabel: "Duration", Text: formatDuration(duration)},
			})
		}
	default:
		widgets = append(widgets, googleChatWidget{
			DecoratedText: &googleChatDecoratedText{TopLabel: "Cause", Text: getFailureReason(payload.Incident)},
		}, googleChatWidget{
			DecoratedText: &googleChatDecoratedText{
				TopLabel: "Failure Count",
				Text:     strconv.Itoa(payload.Incident.FailureCount),
			},
		})
	}

	return widgets
}

func (s *GoogleChatSender) resolvedSubtitle(payload *Payload) string {
	if payload.Incident.ResolvedAt != nil {
		duration := payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt)

		return "Recovered after " + formatDuration(duration)
	}

	return "Incident resolved"
}
