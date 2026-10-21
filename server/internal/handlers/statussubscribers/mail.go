package statussubscribers

import (
	"fmt"
	"net/url"
	"strings"
)

// MailKind identifies which subscriber message to render.
type MailKind string

const (
	// MailKindConfirm is the double opt-in confirmation request.
	MailKindConfirm MailKind = "confirm"
	// MailKindIncidentOpened is the first update for a new incident.
	MailKindIncidentOpened MailKind = "incident-opened"
	// MailKindUpdate is a follow-up update on an ongoing incident/page.
	MailKindUpdate MailKind = "update"
	// MailKindResolved announces an incident has been resolved.
	MailKindResolved MailKind = "resolved"
)

// MailData carries the fields needed to render a subscriber message.
type MailData struct {
	PageName     string
	Title        string
	BodyMarkdown string
	// ConfirmURL is set only for the confirm message.
	ConfirmURL string
	// UnsubscribeURL is set on every non-confirm message.
	UnsubscribeURL string
	// LinkURL is an optional "more info" link from the status update.
	LinkURL string
}

// confirmURL builds the public confirm link from the base URL and token.
func confirmURL(baseURL, token string) string {
	return fmt.Sprintf("%s/api/v1/public/status-subscribers/confirm?token=%s",
		strings.TrimRight(baseURL, "/"), url.QueryEscape(token))
}

// unsubscribeURL builds the one-click unsubscribe link.
func unsubscribeURL(baseURL, token string) string {
	return fmt.Sprintf("%s/api/v1/public/status-subscribers/unsubscribe?token=%s",
		strings.TrimRight(baseURL, "/"), url.QueryEscape(token))
}

// subject returns the email subject line for a message kind. Every update
// about one incident shares the same subject so mail clients keep the whole
// story in one conversation; the state (new, update, resolved) is in the
// preheader and the body.
func (k MailKind) subject(data *MailData) string {
	if k == MailKindConfirm {
		return "Confirm your subscription to " + data.PageName
	}

	return fmt.Sprintf("%s: %s", data.PageName, data.Title)
}

// label returns a human-readable banner for non-confirm messages.
func (k MailKind) label() string {
	switch k {
	case MailKindIncidentOpened:
		return "New incident"
	case MailKindResolved:
		return "Resolved"
	case MailKindConfirm:
		return "Confirm"
	case MailKindUpdate:
		return "Status update"
	default:
		return "Status update"
	}
}
