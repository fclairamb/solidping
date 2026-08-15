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

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

const msTeamsTimeout = 30 * time.Second

// Adaptive Card style colors used for the title TextBlock.
const (
	msTeamsColorAttention = "Attention"
	msTeamsColorGood      = "Good"
)

// msTeamsFieldMonitor is the FactSet label for the check name + type, distinct
// from the "Check" label used by the Google Chat / Mattermost senders — this
// is what the spec calls for on the Teams card.
const msTeamsFieldMonitor = "Monitor"

// msTeamsMessageType is the top-level envelope "type" for a Teams Workflow
// payload — always the literal "message" per the Bot Framework activity
// schema (distinct from the AdaptiveCard "type": "AdaptiveCard" nested inside
// the attachment content).
const msTeamsMessageType = "message"

var (
	// ErrMSTeamsWebhookURLNotConfigured is returned when the Teams webhook URL is missing.
	ErrMSTeamsWebhookURLNotConfigured = errors.New("microsoft teams webhook URL not configured")
	// errMSTeamsWebhookFailed is returned when the Teams webhook request fails.
	errMSTeamsWebhookFailed = errors.New("microsoft teams webhook failed")
)

// MSTeamsSender sends notifications to a Microsoft Teams channel via a Teams
// Workflow ("When a Teams webhook request is received" trigger, Power
// Automate). Microsoft retired the legacy Office 365 Connector / MessageCard
// format; this sender POSTs the modern Adaptive Card envelope instead. Any
// 2xx response (Workflows typically answers 202 Accepted) is treated as
// success, same convention as googlechat.go / mattermost.go.
type MSTeamsSender struct{}

// Send sends a notification to Microsoft Teams.
func (s *MSTeamsSender) Send(ctx context.Context, _ *jobdef.JobContext, payload *Payload) error {
	settings, err := s.parseSettings(payload)
	if err != nil {
		return err
	}

	msg := s.buildMessage(payload)

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling microsoft teams payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, settings.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating microsoft teams request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", productName)

	client := newHTTPClient(msTeamsTimeout)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sending microsoft teams webhook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)

		return fmt.Errorf("%w: status %d: %s", errMSTeamsWebhookFailed, resp.StatusCode, string(respBody))
	}

	return nil
}

func (s *MSTeamsSender) parseSettings(payload *Payload) (*models.MSTeamsSettings, error) {
	settings, err := models.MSTeamsSettingsFromJSONMap(payload.Integration.Settings)
	if err != nil {
		return nil, fmt.Errorf("parsing microsoft teams settings: %w", err)
	}

	if settings.WebhookURL == "" {
		return nil, ErrMSTeamsWebhookURLNotConfigured
	}

	return settings, nil
}

// msTeamsMessage is the modern Teams Workflow envelope: a message carrying a
// single Adaptive Card attachment. The legacy MessageCard format is not
// supported — connector URLs are dead.
type msTeamsMessage struct {
	Type        string              `json:"type"`
	Attachments []msTeamsAttachment `json:"attachments"`
}

type msTeamsAttachment struct {
	ContentType string             `json:"contentType"`
	Content     msTeamsCardContent `json:"content"`
}

type msTeamsCardContent struct {
	Type    string                `json:"type"`
	Version string                `json:"version"`
	Body    []msTeamsBodyElement  `json:"body"`
	MSTeams *msTeamsWidthSettings `json:"msteams,omitempty"`
}

// msTeamsWidthSettings sets the card to full width in the channel so the
// FactSet isn't squeezed into the narrow default card width.
type msTeamsWidthSettings struct {
	Width string `json:"width"`
}

// msTeamsBodyElement is a single AdaptiveCard body element. It doubles as
// both a TextBlock (Text/Size/Weight/Color/Wrap) and a FactSet (Facts) since
// the card body here only ever needs those two element kinds — omitempty
// keeps the marshaled JSON to just the fields relevant for Type.
type msTeamsBodyElement struct {
	Type   string        `json:"type"`
	Text   string        `json:"text,omitempty"`
	Size   string        `json:"size,omitempty"`
	Weight string        `json:"weight,omitempty"`
	Color  string        `json:"color,omitempty"`
	Wrap   bool          `json:"wrap,omitempty"`
	Facts  []msTeamsFact `json:"facts,omitempty"`
}

type msTeamsFact struct {
	Title string `json:"title"`
	Value string `json:"value"`
}

func (s *MSTeamsSender) buildMessage(payload *Payload) *msTeamsMessage {
	checkName := getCheckName(payload.Check)
	title, color := s.eventTitleAndColor(payload, checkName)

	titleBlock := msTeamsBodyElement{
		Type:   "TextBlock",
		Text:   title,
		Size:   "Large",
		Weight: "Bolder",
		Color:  color,
		Wrap:   true,
	}

	factSet := msTeamsBodyElement{
		Type:  "FactSet",
		Facts: s.buildFacts(payload, checkName),
	}

	return &msTeamsMessage{
		Type: msTeamsMessageType,
		Attachments: []msTeamsAttachment{
			{
				ContentType: "application/vnd.microsoft.card.adaptive",
				Content: msTeamsCardContent{
					Type:    "AdaptiveCard",
					Version: "1.4",
					Body:    []msTeamsBodyElement{titleBlock, factSet},
					MSTeams: &msTeamsWidthSettings{Width: "Full"},
				},
			},
		},
	}
}

// eventTitleAndColor returns the title TextBlock text and Adaptive Card
// semantic color for the event type, mirroring GoogleChatSender.buildMessage.
func (s *MSTeamsSender) eventTitleAndColor(payload *Payload, checkName string) (string, string) {
	switch payload.EventType {
	case eventTypeIncidentCreated:
		return "[DOWN] " + checkName, msTeamsColorAttention
	case eventTypeIncidentResolved:
		return "[RECOVERED] " + checkName, msTeamsColorGood
	case eventTypeIncidentEscalated:
		return "[ESCALATED] " + checkName, msTeamsColorAttention
	case eventTypeIncidentReopened:
		return fmt.Sprintf("[REOPENED] %s (relapse #%d)", checkName, payload.Incident.RelapseCount), msTeamsColorAttention
	default:
		return "[UPDATE] " + checkName, ""
	}
}

// buildFacts builds the FactSet facts: Monitor + Duration for a resolved
// incident, or Monitor + Cause + Failure Count otherwise.
func (s *MSTeamsSender) buildFacts(payload *Payload, checkName string) []msTeamsFact {
	facts := []msTeamsFact{
		{Title: msTeamsFieldMonitor, Value: checkName + " (" + payload.Check.Type + ")"},
	}

	if payload.EventType == eventTypeIncidentResolved {
		if payload.Incident.ResolvedAt != nil {
			duration := payload.Incident.ResolvedAt.Sub(payload.Incident.StartedAt)
			facts = append(facts, msTeamsFact{Title: mmFieldDuration, Value: formatDuration(duration)})
		}

		return facts
	}

	facts = append(facts,
		msTeamsFact{Title: mmFieldCause, Value: getFailureReason(payload.Incident)},
		msTeamsFact{Title: mmFieldFailureCount, Value: strconv.Itoa(payload.Incident.FailureCount)},
	)

	return facts
}
