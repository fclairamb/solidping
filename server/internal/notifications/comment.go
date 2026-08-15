package notifications

import "strings"

// commentEmoji is the product-wide identity of `incident.comment`, shared with
// the dash0 event registry (web/dash0/src/components/dashboard/event-display.tsx).
// One emoji per event type product-wide — change both sides together.
const commentEmoji = "💬"

// maxCommentPreview bounds how much of a comment a length-capped channel (SMS
// title, push notification, ntfy body) renders. The dashboard always has the
// full text; a channel summary only has to carry enough to make someone look.
const maxCommentPreview = 280

// CommentInfo is the content of an `incident.comment` event, carried on the
// notification payload so senders render exactly what was written without a
// second lookup of the event row.
type CommentInfo struct {
	// Text is the comment body, already trimmed and length-validated by the
	// incident service.
	Text string `json:"text"`
	// AuthorName is the human label of whoever wrote it — an org member's name
	// or email, a Slack display name, or a Telegram first name. Empty when the
	// author could not be resolved.
	AuthorName string `json:"authorName,omitempty"`
	// Source is where the comment was typed: "web", "slack" or "telegram".
	Source string `json:"source,omitempty"`
}

// commentAuthor returns the author label, or a neutral fallback when the
// author could not be resolved. Never empty, so no sender has to special-case
// an anonymous comment.
func commentAuthor(comment *CommentInfo) string {
	if comment == nil {
		return "Someone"
	}

	if name := strings.TrimSpace(comment.AuthorName); name != "" {
		return name
	}

	return "Someone"
}

// commentText returns the comment body, or "" when the payload carries none
// (which should not happen for an `incident.comment` job, but a sender must
// never render the literal word "<nil>" into somebody's channel).
func commentText(comment *CommentInfo) string {
	if comment == nil {
		return ""
	}

	return strings.TrimSpace(comment.Text)
}

// commentPreview is commentText truncated for length-capped surfaces.
func commentPreview(comment *CommentInfo) string {
	text := commentText(comment)
	if len(text) <= maxCommentPreview {
		return text
	}

	return text[:maxCommentPreview] + "…"
}

// commentTitle is the one-line header a comment notification carries, e.g.
// "💬 Comment on api-health (#42)". Shared by every sender so the same event
// reads the same way whichever channel it lands in.
func commentTitle(payload *Payload) string {
	name := getCheckName(payload.Check)
	ref := strings.TrimSpace(incidentRefPrefix(payload.Incident))

	if ref != "" {
		return commentEmoji + " Comment on " + name + " (" + ref + ")"
	}

	return commentEmoji + " Comment on " + name
}

// commentPlainBody is the plain-text rendering used by channels with no markup
// (SMS-like bodies, ntfy, push). Author line then the comment itself.
func commentPlainBody(payload *Payload) string {
	author := commentAuthor(payload.Comment)
	text := commentText(payload.Comment)

	if text == "" {
		return author + " commented"
	}

	return author + ": " + text
}

// isCommentEvent reports whether a payload is an incident comment.
func isCommentEvent(payload *Payload) bool {
	return payload.EventType == eventTypeIncidentComment
}

// commentSourceLabel renders where the comment came from, for surfaces that
// have room for it. Empty for the dashboard, which is the unremarkable case.
func commentSourceLabel(comment *CommentInfo) string {
	if comment == nil {
		return ""
	}

	switch comment.Source {
	case "slack":
		return "via Slack"
	case "telegram":
		return "via Telegram"
	default:
		return ""
	}
}
