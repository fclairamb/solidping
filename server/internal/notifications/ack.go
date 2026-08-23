package notifications

import "strings"

// ackEmoji is the product-wide identity of `incident.acknowledged`, shared
// with the dash0 event registry
// (web/dash0/src/components/dashboard/event-display.tsx) and
// telegram.StateEmoji. One emoji per event type product-wide — change every
// side together.
const ackEmoji = "✅"

// Field labels shared by every card/attachment-style sender, so an
// acknowledgment reads identically whichever channel it lands in.
const (
	fieldLabelAcknowledgedBy = "Acknowledged by"
	fieldLabelVia            = "Via"
)

// ackActorUnknown is what an acknowledgment with no recoverable identity
// renders as on a channel. Kept in sync with the API's own fallback
// (incidents.ackActorUnknownName) so an operator reading Slack and an operator
// reading the dashboard see the same word.
const ackActorUnknown = "Someone"

// AckInfo is the attribution of an `incident.acknowledged` event, carried on
// the notification payload so senders render who took the incident without a
// second lookup of the event row.
//
// Embedded in the job config rather than re-read at send time for the same
// reason CommentInfo is: the notice then names exactly the person who acked,
// even if the incident is unacked (or re-acked by somebody else) between the
// enqueue and the delivery.
type AckInfo struct {
	// ActorName is the human label of whoever acknowledged — an org member's
	// name or email, a Slack/Discord username, a Telegram first name, or the
	// phone number that dialed in. Empty when nothing could be resolved.
	ActorName string `json:"actorName,omitempty"`
	// Via is the channel the acknowledgment came from ("web", "slack",
	// "discord", "telegram", "email", "phone").
	Via string `json:"via,omitempty"`
}

// ackActor returns the actor label, or a neutral fallback. Never empty, so no
// sender has to special-case an unattributed acknowledgment.
func ackActor(ack *AckInfo) string {
	if ack == nil {
		return ackActorUnknown
	}

	if name := strings.TrimSpace(ack.ActorName); name != "" {
		return name
	}

	return ackActorUnknown
}

// ackViaLabel renders where the acknowledgment came from, e.g. "via Slack".
// Empty when the channel is unknown or is the dashboard — "via web" is noise
// nobody needs, exactly like an unremarkable comment source.
func ackViaLabel(ack *AckInfo) string {
	if ack == nil {
		return ""
	}

	switch strings.ToLower(strings.TrimSpace(ack.Via)) {
	case "slack":
		return "via Slack"
	case "discord":
		return "via Discord"
	case "telegram":
		return "via Telegram"
	case "email":
		return "via email"
	case "phone":
		return "via phone"
	default:
		return ""
	}
}

// ackViaName is ackViaLabel without the leading "via", for the surfaces that
// render it as a labeled field rather than in a sentence.
func ackViaName(ack *AckInfo) string {
	return strings.TrimPrefix(ackViaLabel(ack), "via ")
}

// ackTitle is the one-line header an acknowledgment notice carries, e.g.
// "✅ Acknowledged: api-health (#42)". Shared by every sender so the same
// event reads the same way whichever channel it lands in.
func ackTitle(payload *Payload) string {
	name := getCheckName(payload.Check)
	ref := strings.TrimSpace(incidentRefPrefix(payload.Incident))

	if ref != "" {
		return ackEmoji + " Acknowledged: " + name + " (" + ref + ")"
	}

	return ackEmoji + " Acknowledged: " + name
}

// ackPlainBody is the plain-text rendering used by channels with no markup
// (SMS-like bodies, ntfy, push): who took it, and from where.
func ackPlainBody(payload *Payload) string {
	body := ackActor(payload.Acknowledgment) + " acknowledged this incident"

	if via := ackViaLabel(payload.Acknowledgment); via != "" {
		body += " " + via
	}

	return body + "."
}

// ackSentence is ackPlainBody's markup-free core without the trailing period,
// for senders that compose it into a larger line.
func ackSentence(payload *Payload) string {
	return strings.TrimSuffix(ackPlainBody(payload), ".")
}
