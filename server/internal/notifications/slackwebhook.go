package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/httpclientpool"
	"github.com/fclairamb/solidping/server/internal/integrations/slack"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
)

// slackBlockTypeActions is the Block Kit type of an interactive button row.
const slackBlockTypeActions = "actions"

const slackWebhookTimeout = 30 * time.Second

var (
	// ErrSlackWebhookURLNotConfigured is returned when the Slack webhook URL is missing.
	ErrSlackWebhookURLNotConfigured = errors.New("slack webhook URL not configured")
	// errSlackWebhookFailed is returned when the Slack webhook request fails.
	errSlackWebhookFailed = errors.New("slack webhook failed")
)

// slackAckPrompt is the call to action of the app's alert card; it points at
// buttons a webhook cannot carry, so the webhook card drops it.
const slackAckPrompt = "Please acknowledge the incident."

// SlackWebhookSender sends one-way notifications to a Slack incoming webhook.
//
// Unlike SlackSender it needs no Slack app: no threads (a webhook returns no
// ts), no interactive buttons, no mentions of Slack user ids. Every event is a
// standalone message carrying the incident reference.
type SlackWebhookSender struct{}

// Send sends a notification to a Slack incoming webhook.
func (s *SlackWebhookSender) Send(ctx context.Context, jctx *jobdef.JobContext, payload *Payload) error {
	webhookURL, err := s.parseSettings(payload)
	if err != nil {
		return err
	}

	guard := egressGuardFrom(jctx)
	if urlErr := ValidateSenderURL(ctx, guard, webhookURL); urlErr != nil {
		return urlErr
	}

	body, err := json.Marshal(s.buildMessage(payload))
	if err != nil {
		return fmt.Errorf("marshaling slack webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating slack webhook request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", productName)

	resp, err := httpclientpool.NewGuardedClient(slackWebhookTimeout, guard).Do(req)
	if err != nil {
		return fmt.Errorf("sending slack webhook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)

		return fmt.Errorf("%w: status %d: %s", errSlackWebhookFailed, resp.StatusCode, string(respBody))
	}

	return nil
}

type slackWebhookSettings struct {
	WebhookURL string `json:"webhook_url"` //nolint:tagliatelle // matches dashboard form key
}

func (s *SlackWebhookSender) parseSettings(payload *Payload) (string, error) {
	data, err := json.Marshal(payload.Integration.Settings)
	if err != nil {
		return "", fmt.Errorf("parsing slack webhook settings: %w", err)
	}

	var settings slackWebhookSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return "", fmt.Errorf("parsing slack webhook settings: %w", err)
	}

	url := webhookURLWithLegacyFallback(settings.WebhookURL, payload.Integration.Settings)
	if url == "" {
		return "", ErrSlackWebhookURLNotConfigured
	}

	return url, nil
}

// buildMessage reuses the Slack app's block builders, then removes what a
// webhook cannot support: on-call mentions (they are Slack user ids), action
// buttons, and the "please acknowledge" prompt that points at them. The
// fallback text is guaranteed to carry the incident reference because there is
// no thread to give the message context.
func (s *SlackWebhookSender) buildMessage(payload *Payload) *slack.MessageResponse {
	stripped := *payload
	stripped.OnCallMentions = nil

	msg := (&SlackSender{}).buildMessage(&stripped)
	msg.Blocks = stripWebhookBlocks(msg.Blocks)

	for i := range msg.Attachments {
		msg.Attachments[i].Blocks = stripWebhookBlocks(msg.Attachments[i].Blocks)
	}

	if ref := incidentRefPrefix(payload.Incident); ref != "" &&
		!strings.Contains(msg.Text, fmt.Sprintf("#%d", payload.Incident.Number)) {
		msg.Text = ref + msg.Text
	}

	return msg
}

func stripWebhookBlocks(blocks []slack.Block) []slack.Block {
	if blocks == nil {
		return nil
	}

	out := make([]slack.Block, 0, len(blocks))

	for i := range blocks {
		if blocks[i].Type == slackBlockTypeActions {
			continue
		}

		if blocks[i].Text != nil && blocks[i].Text.Text == slackAckPrompt {
			continue
		}

		out = append(out, blocks[i])
	}

	return out
}
