// Package webpush provides Web Push notification sending support (VAPID + Push API).
package webpush

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	webpushgo "github.com/SherClockHolmes/webpush-go"
)

// ErrSubscriptionGone is returned when the push service reports the
// subscription is no longer valid (HTTP 404 or 410). The caller must
// soft-delete the corresponding UserContact or prune it from channel Settings.
var ErrSubscriptionGone = errors.New("webpush: subscription gone")

// Message is the payload sent via Web Push.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"` // incident or check URL to open on click
}

// Options holds the VAPID credentials and subject required for signing pushes.
type Options struct {
	VAPIDPublicKey  string
	VAPIDPrivateKey string
	// Subject is the VAPID subject — a mailto: or URL string as required by the spec.
	Subject string
}

// Send encrypts msg and POSTs it to the push service endpoint encoded in
// subscriptionJSON (a JSON-serialised PushSubscription from the browser).
// Returns ErrSubscriptionGone for 404/410 responses.
func Send(ctx context.Context, opts Options, subscriptionJSON string, msg Message) error {
	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("webpush: marshal message: %w", err)
	}

	sub := &webpushgo.Subscription{}
	if err := json.Unmarshal([]byte(subscriptionJSON), sub); err != nil {
		return fmt.Errorf("webpush: parse subscription: %w", err)
	}

	resp, err := webpushgo.SendNotificationWithContext(ctx, payload, sub, &webpushgo.Options{
		VAPIDPublicKey:  opts.VAPIDPublicKey,
		VAPIDPrivateKey: opts.VAPIDPrivateKey,
		Subscriber:      opts.Subject,
		TTL:             86400,
	})
	if err != nil {
		return fmt.Errorf("webpush: send: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return ErrSubscriptionGone
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webpush: push service returned %d", resp.StatusCode)
	}

	return nil
}
