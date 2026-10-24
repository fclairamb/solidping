package emailpreview

// Preview fixtures of the incident-* templates: the incident lifecycle, SLO
// burn and degraded alerts. Each one is a notification payload rendered
// through notifications.IncidentEmailViewModel, the view model the mailer
// builds, so a preview cannot drift from a real mail (a hand-written "via
// Slack" or a pre-formatted date would).

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/notifications"
)

const (
	fixtureBaseURL      = "https://solidping.example"
	fixtureCheckUID     = "3f7a9c2e-6b1d-4e0a-9c8f-1a2b3c4d5e6f"
	fixtureAckURL       = fixtureBaseURL + "/api/v1/orgs/acme/incidents/" + fixtureIncidentUID + "/ack?token=preview-token"
	fixtureUnsubURL     = fixtureBaseURL + "/unsubscribe?token=preview-unsub-token"
	fixtureFailureCause = "HTTP 503 Service Unavailable (expected 200)"
	eventCreated        = "incident.created"
	eventResolved       = "incident.resolved"
	// fixtureChannel is where the fixtures' acknowledgments and comment come
	// from: a channel the mails name ("via Slack").
	fixtureChannel = "slack"
)

// fixtureStartedAt is when every fixture incident started: Jul 5, 10:00 UTC
// of this year, so the ack link expires on fixtureAckExpiresAt and no preview
// shows a year.
func fixtureStartedAt() time.Time {
	return fixtureAckExpiresAt().Add(-7 * 24 * time.Hour)
}

// fixturePayload is an open check incident on the fixture check, failing from
// one region of three.
func fixturePayload(eventType string) *notifications.Payload {
	name := fixtureCheckName

	return &notifications.Payload{
		EventType: eventType,
		Check: &models.Check{
			UID: fixtureCheckUID, Name: &name, Type: "http",
			Config: models.JSONMap{"url": "https://api.acme.com/health"},
		},
		Incident: &models.Incident{
			UID: fixtureIncidentUID, Number: fixtureIncidentNumber, StartedAt: fixtureStartedAt(),
			FailureCount: 3, RelapseCount: 1,
			Details: models.JSONMap{"failure_reason": fixtureFailureCause},
		},
		Regions: &notifications.RegionInfo{
			Failing: []string{"eu-west"}, RegionCount: 3, FailQuorum: 1,
			Labels: map[string]notifications.RegionLabel{"eu-west": {Emoji: "🇪🇺", Name: "Europe West"}},
		},
		OrgSlug:    "acme",
		AppBaseURL: fixtureBaseURL,
	}
}

// resolve closes the fixture incident after span.
func resolve(payload *notifications.Payload, span time.Duration) {
	resolvedAt := payload.Incident.StartedAt.Add(span)
	payload.EventType = eventResolved
	payload.Incident.ResolvedAt = &resolvedAt
}

// render builds a payload's view model the way the mailer does, at the
// incident start (only a reminder reads the clock, see reminderIncidentFixture).
func render(loc i18n.Locale, payload *notifications.Payload, ackURL string) map[string]any {
	return notifications.IncidentEmailViewModel(loc, payload, ackURL, fixtureUnsubURL, payload.Incident.StartedAt)
}

// incidentFixture is shared by created/escalated/reopened: they read the same
// field set.
func incidentFixture(loc i18n.Locale) map[string]any {
	return render(loc, fixturePayload(eventCreated), fixtureAckURL)
}

// resolvedIncidentFixture is the recovery after 15 minutes: nothing to ack.
func resolvedIncidentFixture(loc i18n.Locale) map[string]any {
	payload := fixturePayload(eventCreated)
	resolve(payload, 15*time.Minute)

	return render(loc, payload, "")
}

// burnPayload is an SLO burn-rate alert: a burn incident IS an incident (same
// ack link, same deep links) carrying the three deciding numbers in its
// details, written the way the burn evaluator writes them.
func burnPayload(eventType string) *notifications.Payload {
	payload := fixturePayload(eventType)
	payload.Regions = nil
	payload.Incident.Kind = models.IncidentKindSLOBurn
	payload.Incident.Details = models.JSONMap{
		"slo_name":                 "Acme API availability",
		"slo_alert_policy_kind":    models.SLOAlertPolicyKindFast,
		"severity":                 models.SLOAlertSeverityCritical,
		"burn_threshold":           14.4,
		"burn_rate_long":           31.0,
		"burn_rate_short":          44.5,
		"burn_peak":                52.0,
		"long_window_seconds":      3600.0,
		"short_window_seconds":     300.0,
		"budget_remaining_seconds": 5400.0,
		"projected_exhaustion_at":  fixtureStartedAt().Add(4*time.Hour + 30*time.Minute).Format(time.RFC3339),
		"target_pct":               99.9,
	}

	return payload
}

func burnIncidentFixture(loc i18n.Locale) map[string]any {
	return render(loc, burnPayload(eventCreated), fixtureAckURL)
}

// resolvedBurnIncidentFixture is the cleared alert: no ack, and a rate back
// under the threshold.
func resolvedBurnIncidentFixture(loc i18n.Locale) map[string]any {
	payload := burnPayload(eventCreated)
	payload.Incident.Details["burn_rate_long"] = 1.2
	resolve(payload, 15*time.Minute)

	return render(loc, payload, "")
}

// degradedPayload is the degraded-detection notice (spec 2026-09-22-03) with
// the motivating episode's numbers: 7 failures in 60 probes, 93.8%
// availability, slow probes too, and a target that is up at this very moment.
func degradedPayload() *notifications.Payload {
	payload := fixturePayload(eventCreated)
	payload.Regions = nil
	payload.Incident.Kind = models.IncidentKindDegraded
	payload.Incident.Details = models.JSONMap{
		"degraded_failures_fired":   true,
		"degraded_failure_count":    7.0,
		"degraded_failure_slots":    60.0,
		"degraded_failures":         5.0,
		"degraded_failures_window":  60.0,
		"degraded_availability_pct": 93.8,
		"degraded_slow_fired":       true,
		"degraded_slow_count":       3.0,
		"degraded_slow_slots":       6.0,
		"degraded_slow":             3.0,
		"degraded_slow_window":      6.0,
		"slow_threshold_ms":         1000.0,
		"degraded_window_from":      fixtureStartedAt().Add(-53 * time.Minute).Format(time.RFC3339),
		"degraded_window_to":        fixtureStartedAt().Format(time.RFC3339),
		"degraded_currently_up":     true,
	}

	return payload
}

func degradedIncidentFixture(loc i18n.Locale) map[string]any {
	return render(loc, degradedPayload(), fixtureAckURL)
}

// resolvedDegradedIncidentFixture is the cleared half: the pattern stopped, so
// there is nothing left to acknowledge.
func resolvedDegradedIncidentFixture(loc i18n.Locale) map[string]any {
	payload := degradedPayload()
	resolve(payload, 53*time.Minute)

	return render(loc, payload, "")
}

// acknowledgedIncidentFixture is the "somebody took it" half of the lifecycle,
// from Slack: the incident is still open, but there is no ack link left.
func acknowledgedIncidentFixture(loc i18n.Locale) map[string]any {
	payload := fixturePayload("incident.acknowledged")
	acknowledgedAt := payload.Incident.StartedAt.Add(4 * time.Minute)
	payload.Incident.AcknowledgedAt = &acknowledgedAt
	payload.Acknowledgment = &notifications.AckInfo{ActorName: fixturePersonName, Via: fixtureChannel}

	return render(loc, payload, "")
}

// unacknowledgedIncidentFixture is the retraction: open and unowned again, so
// the ack link IS present. Taking the incident from this mail is the point.
func unacknowledgedIncidentFixture(loc i18n.Locale) map[string]any {
	payload := fixturePayload("incident.unacknowledged")
	payload.Acknowledgment = &notifications.AckInfo{ActorName: fixturePersonName, Via: fixtureChannel}

	return render(loc, payload, fixtureAckURL)
}

// reminderIncidentFixture is the third reminder of an incident open for
// 1 h 15 min and still unacknowledged (spec 2026-10-12-16).
func reminderIncidentFixture(loc i18n.Locale) map[string]any {
	payload := fixturePayload("incident.reminder")
	payload.ReminderSeq = 3

	return notifications.IncidentEmailViewModel(loc, payload, fixtureAckURL, fixtureUnsubURL,
		payload.Incident.StartedAt.Add(75*time.Minute))
}

// commentIncidentFixture carries a deliberately multi-line comment, posted
// from Slack, so the preview exercises the quote block's wrapping rather than
// a single short line.
func commentIncidentFixture(loc i18n.Locale) map[string]any {
	payload := fixturePayload("incident.comment")
	payload.Comment = &notifications.CommentInfo{
		AuthorName: "Bob Builder",
		Source:     fixtureChannel,
		Text: "Upstream provider confirms a regional outage.\n" +
			"Failing over to the secondary region now, next update in 15 minutes.",
	}

	return render(loc, payload, "")
}
