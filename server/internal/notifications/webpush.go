package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

// WebPushSender sends a Web Push notification to every subscribed browser
// stored in the channel's Settings.subscriptions array.
//
// Dead subscriptions (push service returns 404 / 410) are pruned from the
// settings array and persisted via the injected UpdateChannel callback.
// All other errors are logged and skipped (best-effort fan-out).
type WebPushSender struct {
	// UpdateChannel, when non-nil, is called after pruning to persist the
	// updated subscriptions list back to the channel row. Wired by the
	// notification job runner.
	UpdateChannel func(ctx context.Context, channel *models.Integration) error
}

// webPushSubscription is the per-subscription shape stored in
// Settings["subscriptions"].
type webPushSubscription struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
	Label string `json:"label,omitempty"`
}

// ErrWebPushNotConfigured is returned when VAPID keys are absent.
var ErrWebPushNotConfigured = errors.New("web push not configured")

// Send delivers a push notification to all subscribed browsers for the channel.
func (s *WebPushSender) Send(ctx context.Context, jctx *jobdef.JobContext, payload *Payload) error {
	if jctx == nil || jctx.Services == nil || jctx.Services.WebPushOptions.VAPIDPublicKey == "" {
		return ErrWebPushNotConfigured
	}

	subs, err := parseSubscriptions(payload.Integration.Settings)
	if err != nil {
		return err
	}

	if len(subs) == 0 {
		return nil // no subscriptions — nothing to send
	}

	opts := jctx.Services.WebPushOptions
	checkName := getCheckName(payload.Check)

	title, body := buildWebPushContent(payload, checkName)

	msg := webpush.Message{
		Title: title,
		Body:  body,
		URL:   buildWebPushURL(payload),
	}

	var goneEndpoints []string

	for i := range subs {
		subJSON, marshalErr := json.Marshal(subs[i])
		if marshalErr != nil {
			slog.WarnContext(ctx, "webpush: failed to marshal subscription; skipping",
				"endpoint", subs[i].Endpoint, "error", marshalErr)

			continue
		}

		sendErr := webpush.Send(ctx, opts, string(subJSON), msg)

		switch {
		case errors.Is(sendErr, webpush.ErrSubscriptionGone):
			slog.InfoContext(ctx, "webpush: subscription gone; queuing for pruning",
				"endpoint", subs[i].Endpoint)

			goneEndpoints = append(goneEndpoints, subs[i].Endpoint)
		case sendErr != nil:
			// Non-fatal: log and continue for the remaining subscriptions.
			slog.WarnContext(ctx, "webpush: send failed; skipping subscription",
				"endpoint", subs[i].Endpoint, "error", sendErr)
		}
	}

	if len(goneEndpoints) > 0 {
		pruneSubscriptions(payload.Integration, goneEndpoints)

		if s.UpdateChannel != nil {
			if updateErr := s.UpdateChannel(ctx, payload.Integration); updateErr != nil {
				slog.WarnContext(ctx, "webpush: failed to persist pruned subscriptions",
					"error", updateErr)
			}
		}
	}

	return nil
}

// parseSubscriptions unmarshals the "subscriptions" key from channel settings.
func parseSubscriptions(settings models.JSONMap) ([]webPushSubscription, error) {
	raw, ok := settings["subscriptions"]
	if !ok {
		return nil, nil
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("marshal subscriptions: %w", err)
	}

	var subs []webPushSubscription
	if unmarshalErr := json.Unmarshal(data, &subs); unmarshalErr != nil {
		return nil, fmt.Errorf("unmarshal subscriptions: %w", unmarshalErr)
	}

	return subs, nil
}

// pruneSubscriptions removes the listed endpoints from the channel settings.
func pruneSubscriptions(channel *models.Integration, goneEndpoints []string) {
	gone := make(map[string]bool, len(goneEndpoints))
	for _, e := range goneEndpoints {
		gone[e] = true
	}

	existing, _ := parseSubscriptions(channel.Settings)

	remaining := make([]webPushSubscription, 0, len(existing))

	for i := range existing {
		if !gone[existing[i].Endpoint] {
			remaining = append(remaining, existing[i])
		}
	}

	channel.Settings["subscriptions"] = remaining
}

// buildWebPushURL returns the in-app path the notification should open on
// click — the incident detail page when an incident is present, otherwise the
// org dashboard. A non-empty value is also what gives each incident a distinct
// service-worker notification tag (the SW derives the tag from this URL), so
// updates for different incidents don't collapse into one notification.
func buildWebPushURL(payload *Payload) string {
	if payload.OrgSlug == "" {
		return ""
	}

	base := "/dash0/orgs/" + payload.OrgSlug

	if payload.Incident != nil && payload.Incident.UID != "" {
		return base + "/incidents/" + payload.Incident.UID
	}

	return base
}

// buildWebPushContent returns (title, body) for a push notification. Titles
// carry the short #N incident reference (incidentRefPrefix, same as Slack and
// Discord) — commentTitle already includes it on the comment path.
func buildWebPushContent(payload *Payload, checkName string) (string, string) {
	ref := incidentRefPrefix(payload.Incident)

	switch payload.EventType {
	case eventTypeIncidentCreated:
		return ref + "[DOWN] " + checkName,
			fmt.Sprintf("%s is down. Cause: %s", checkName, getFailureReason(payload.Incident))
	case eventTypeIncidentResolved:
		return ref + "[RECOVERED] " + checkName,
			checkName + " is back up."
	case eventTypeIncidentEscalated:
		return ref + "[ESCALATED] " + checkName,
			fmt.Sprintf("Incident for %s has been escalated.", checkName)
	case eventTypeIncidentReopened:
		return ref + "[REOPENED] " + checkName,
			checkName + " is down again."
	case eventTypeIncidentComment:
		return commentTitle(payload), commentPreview(payload.Comment)
	default:
		return ref + "[UPDATE] " + checkName,
			"An incident update occurred for " + checkName
	}
}
