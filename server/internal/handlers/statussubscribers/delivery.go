package statussubscribers

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
	"github.com/fclairamb/solidping/server/internal/servicesig"
)

// Delivery tuning.
const (
	// deliveryTimeout bounds one webhook POST. The fan-out is sequential, so a
	// slow endpoint delays every subscriber behind it; ten seconds is generous
	// for a webhook receiver and short enough that one dead host cannot stall a
	// publication for minutes.
	deliveryTimeout = 10 * time.Second
	// maxFailuresBeforeDisable is the circuit breaker. It counts CONSECUTIVE
	// failures — one success resets it — so a receiver that blips during a
	// deploy is never disabled, while one that has been gone for five straight
	// publications is.
	maxFailuresBeforeDisable = 5
	// deliveryKeyID is the X-SP-Key-Id a status-page webhook advertises. The
	// key is per-subscriber, so there is nothing to rotate BETWEEN keys and the
	// id is a constant naming the scheme rather than a key generation.
	deliveryKeyID = "status-page-subscriber"
	// maxErrorBodyBytes caps how much of a failing receiver's response body is
	// read for the log line. A misconfigured endpoint answering with a 5 MB
	// HTML error page must not end up in the server log.
	maxErrorBodyBytes = 512
)

// WebhookPayload is the JSON body a `webhook` subscription receives.
//
// It carries only what the PUBLIC status page already shows: the page, the
// update's own text, and a link. No check UID, no probe output, no internal
// hostname — a subscriber webhook is a public surface with a URL for an
// address, and the never-public rule applies to it exactly as it does to the
// page itself.
type WebhookPayload struct {
	Event         string `json:"event"`
	StatusPageUID string `json:"statusPageUid"`
	PageName      string `json:"pageName"`
	// IncidentUID is the PUBLICATION's uid when the update is threaded under an
	// incident — the same opaque id the public incidents endpoint returns, not
	// the internal incident.
	IncidentUID  *string   `json:"incidentUid,omitempty"`
	Kind         string    `json:"kind"`
	Title        string    `json:"title"`
	BodyMarkdown string    `json:"bodyMarkdown"`
	LinkURL      *string   `json:"linkUrl,omitempty"`
	PublishedAt  time.Time `json:"publishedAt"`
}

// slackPayload is the body a `slack` subscription receives — Slack's
// incoming-webhook shape. `text` is set alongside the blocks because it is what
// Slack uses for the notification preview and for clients that cannot render
// blocks.
type slackPayload struct {
	Text   string       `json:"text"`
	Blocks []slackBlock `json:"blocks,omitempty"`
}

type slackBlock struct {
	Type string     `json:"type"`
	Text *slackText `json:"text,omitempty"`
}

type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// deliverEndpoint posts one update to a webhook/slack subscriber and records
// the outcome on the row.
//
// Errors are never returned: the fan-out must not fail the status update that
// triggered it. They are logged, counted, and — past the threshold — turned
// into a disabled subscription plus an event, so an operator can see that a
// delivery stopped and why.
func (n *Notifier) deliverEndpoint(
	ctx context.Context, sub *models.StatusPageSubscriber, event *UpdateEvent,
) {
	if sub.EndpointPrivate == nil {
		n.logger.ErrorContext(ctx, "status subscriber fan-out: endpoint subscriber has no stored endpoint",
			"subscriberUid", sub.UID)

		return
	}

	secrets, err := openEndpoint(ctx, n.creds, sub.OrganizationUID, *sub.EndpointPrivate)
	if err != nil {
		// A endpoint that cannot be opened is a configuration fault, not a
		// receiver fault, so it is NOT counted against the circuit breaker —
		// disabling a subscription because this process lacks the master key
		// would turn a deploy mistake into silent data loss.
		n.logger.ErrorContext(ctx, "status subscriber fan-out: failed to open endpoint",
			"subscriberUid", sub.UID, "error", err)

		return
	}

	body, contentType, err := n.buildDeliveryBody(sub.Channel, event)
	if err != nil {
		n.logger.ErrorContext(ctx, "status subscriber fan-out: failed to build payload",
			"subscriberUid", sub.UID, "error", err)

		return
	}

	err = n.post(ctx, sub, secrets, body, contentType)
	n.recordDeliveryOutcome(ctx, sub, err)
}

// buildDeliveryBody renders the per-channel payload.
func (n *Notifier) buildDeliveryBody(
	channel models.SubscriberChannel, event *UpdateEvent,
) ([]byte, string, error) {
	if channel == models.SubscriberChannelSlack {
		body, err := json.Marshal(slackPayloadFor(event))

		return body, "application/json", err
	}

	body, err := json.Marshal(WebhookPayload{
		Event:         "status_update.published",
		StatusPageUID: event.StatusPageUID,
		PageName:      event.PageName,
		IncidentUID:   event.IncidentUID,
		Kind:          event.Kind,
		Title:         event.Title,
		BodyMarkdown:  event.BodyMarkdown,
		LinkURL:       event.LinkURL,
		PublishedAt:   time.Now().UTC(),
	})

	return body, "application/json", err
}

// slackPayloadFor renders the Slack message. Deliberately plain: a status
// update is operator-authored prose, and wrapping it in attachments/colours
// would be inventing severity the update itself did not state.
func slackPayloadFor(event *UpdateEvent) slackPayload {
	headline := event.PageName + ": " + event.Title

	blocks := []slackBlock{
		{Type: "section", Text: &slackText{Type: "mrkdwn", Text: "*" + headline + "*"}},
	}

	if event.BodyMarkdown != "" {
		blocks = append(blocks, slackBlock{
			Type: "section",
			Text: &slackText{Type: "mrkdwn", Text: event.BodyMarkdown},
		})
	}

	if event.LinkURL != nil && *event.LinkURL != "" {
		blocks = append(blocks, slackBlock{
			Type: "context",
			Text: &slackText{Type: "mrkdwn", Text: *event.LinkURL},
		})
	}

	return slackPayload{Text: headline, Blocks: blocks}
}

// post performs the signed HTTP delivery.
//
// The signature headers are the internal/servicesig scheme, reused verbatim so
// a receiver that already verifies SolidPing's billing calls needs no second
// implementation. Slack deliveries carry them too — Slack ignores unknown
// headers, and sending them unconditionally keeps one code path.
func (n *Notifier) post(
	ctx context.Context, sub *models.StatusPageSubscriber,
	secrets endpointSecrets, body []byte, contentType string,
) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, secrets.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build delivery request: %w", err)
	}

	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "SolidPing-StatusPage/1")

	if secrets.SigningSecret != "" {
		timestamp := time.Now().Unix()
		req.Header.Set(servicesig.HeaderSignature, servicesig.SignaturePrefix+
			servicesig.Sign(servicesig.Key{ID: deliveryKeyID, Secret: secrets.SigningSecret},
				timestamp, http.MethodPost, req.URL.Path, body))
		req.Header.Set(servicesig.HeaderTimestamp, strconv.FormatInt(timestamp, 10))
		req.Header.Set(servicesig.HeaderKeyID, deliveryKeyID)
	}

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("deliver: %w", err)
	}

	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}

	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))

	return fmt.Errorf("%w: %d %s", errDeliveryRejected, resp.StatusCode, string(snippet))
}

// recordDeliveryOutcome resets or increments the failure counter, and trips the
// breaker at the threshold.
func (n *Notifier) recordDeliveryOutcome(
	ctx context.Context, sub *models.StatusPageSubscriber, deliveryErr error,
) {
	if deliveryErr == nil {
		if sub.FailureCount == 0 {
			return
		}

		if err := n.db.UpdateSubscriberDelivery(ctx, sub.UID, 0, nil); err != nil {
			n.logger.ErrorContext(ctx, "status subscriber fan-out: failed to reset failure count",
				"subscriberUid", sub.UID, "error", err)
		}

		return
	}

	failures := sub.FailureCount + 1

	// The endpoint URL is a credential: log the subscriber UID and the error,
	// never the target.
	n.logger.WarnContext(ctx, "status subscriber fan-out: delivery failed",
		"subscriberUid", sub.UID, "channel", string(sub.Channel),
		"consecutiveFailures", failures, "error", deliveryErr)

	var disabledAt *time.Time

	if failures >= maxFailuresBeforeDisable {
		now := time.Now().UTC()
		disabledAt = &now
	}

	if err := n.db.UpdateSubscriberDelivery(ctx, sub.UID, failures, disabledAt); err != nil {
		n.logger.ErrorContext(ctx, "status subscriber fan-out: failed to record delivery failure",
			"subscriberUid", sub.UID, "error", err)

		return
	}

	if disabledAt == nil {
		return
	}

	n.recordDisabledEvent(ctx, sub, failures, deliveryErr)
}

// recordDisabledEvent writes the audit event that makes a silently-stopped
// webhook visible. Without it the only symptom of a disabled subscription is
// "we stopped getting notifications", which is exactly the thing nobody
// notices until an incident.
func (n *Notifier) recordDisabledEvent(
	ctx context.Context, sub *models.StatusPageSubscriber, failures int, deliveryErr error,
) {
	event := models.NewEvent(sub.OrganizationUID, models.EventTypeStatusSubscriberDisabled, models.ActorTypeSystem)
	event.Payload = models.JSONMap{
		"subscriberUid": sub.UID,
		"statusPageUid": sub.StatusPageUID,
		"channel":       string(sub.Channel),
		"failures":      failures,
		"lastError":     deliveryErr.Error(),
	}

	if sub.EndpointHint != nil {
		event.Payload["endpoint"] = *sub.EndpointHint
	}

	if err := n.db.CreateEvent(ctx, event); err != nil {
		n.logger.ErrorContext(ctx, "status subscriber fan-out: failed to record disabled event",
			"subscriberUid", sub.UID, "error", err)
	}
}

// errDeliveryRejected marks a non-2xx response from a receiver — a RECEIVER
// fault, so it counts against the circuit breaker, unlike a local
// configuration fault.
var errDeliveryRejected = errors.New("endpoint rejected the delivery")
