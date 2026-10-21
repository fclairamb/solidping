package notifications

import (
	"strings"

	"github.com/fclairamb/solidping/server/internal/i18n"
)

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

// ackActor returns the actor label, or a neutral fallback ("Someone"). Never
// empty, so no sender has to special-case an unattributed acknowledgment.
func ackActor(ack *AckInfo) string {
	return ackActorIn(i18n.English, ack)
}

// ackActorIn is ackActor in the reader's language (only the fallback changes).
func ackActorIn(loc i18n.Locale, ack *AckInfo) string {
	if ack != nil {
		if name := strings.TrimSpace(ack.ActorName); name != "" {
			return name
		}
	}

	return loc.T("incident.someone")
}

// channelNameIn names the channel an acknowledgment or a comment came from
// ("Slack", "email"), in the reader's language. Empty when the channel is
// unknown or is the dashboard: "via web" is noise nobody needs.
func channelNameIn(loc i18n.Locale, channel string) string {
	if key := channelKey(channel); key != "" {
		return loc.T("incident.channel." + key)
	}

	return ""
}

// channelKey is the catalog key of a channel a mail names, or "" for the
// dashboard and for unknown channels.
func channelKey(channel string) string {
	switch key := strings.ToLower(strings.TrimSpace(channel)); key {
	case "slack", "discord", "telegram", "email", "phone":
		return key
	default:
		return ""
	}
}

// viaPhraseIn is how a sentence says where something came from ("via Slack"),
// in the reader's language. The preposition depends on the channel (French
// says "via Slack" but "par e-mail"), so the whole phrase is one catalog
// entry. Empty exactly when channelNameIn is.
func viaPhraseIn(loc i18n.Locale, channel string) string {
	if key := channelKey(channel); key != "" {
		return loc.T("incident.via." + key)
	}

	return ""
}

// ackChannelIn names the channel an acknowledgment came from, or "".
func ackChannelIn(loc i18n.Locale, ack *AckInfo) string {
	if ack == nil {
		return ""
	}

	return channelNameIn(loc, ack.Via)
}

// ackViaIn says where an acknowledgment came from ("via Slack"), in the
// reader's language, or "".
func ackViaIn(loc i18n.Locale, ack *AckInfo) string {
	if ack == nil {
		return ""
	}

	return viaPhraseIn(loc, ack.Via)
}

// ackViaLabel renders where the acknowledgment came from, e.g. "via Slack".
// Empty when the channel is unknown or is the dashboard, exactly like an
// unremarkable comment source.
func ackViaLabel(ack *AckInfo) string {
	return ackViaIn(i18n.English, ack)
}

// ackViaName is ackViaLabel without the leading "via", for the surfaces that
// render it as a labeled field rather than in a sentence.
func ackViaName(ack *AckInfo) string {
	return ackChannelIn(i18n.English, ack)
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
