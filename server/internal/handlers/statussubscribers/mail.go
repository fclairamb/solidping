package statussubscribers

import (
	"fmt"
	"html"
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

// subject returns the email subject line for a message kind.
func (k MailKind) subject(data *MailData) string {
	switch k {
	case MailKindConfirm:
		return "Confirm your subscription to " + data.PageName
	case MailKindIncidentOpened:
		return fmt.Sprintf("[%s] New incident: %s", data.PageName, data.Title)
	case MailKindResolved:
		return fmt.Sprintf("[%s] Resolved: %s", data.PageName, data.Title)
	case MailKindUpdate:
		return fmt.Sprintf("[%s] Update: %s", data.PageName, data.Title)
	default:
		return fmt.Sprintf("[%s] %s", data.PageName, data.Title)
	}
}

// buildHTML renders the HTML body for a message kind.
func (k MailKind) buildHTML(data *MailData) string {
	var builder strings.Builder

	builder.WriteString(`<!DOCTYPE html><html><head><meta charset="utf-8">`)
	builder.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1.0"></head>`)
	builder.WriteString(`<body style="font-family:-apple-system,Segoe UI,Roboto,Arial,sans-serif;`)
	builder.WriteString(`color:#2d3748;background:#f5f6f8;margin:0;padding:24px 12px;">`)
	builder.WriteString(`<div style="max-width:600px;margin:0 auto;background:#fff;border-radius:8px;`)
	builder.WriteString(`padding:32px;">`)

	if k == MailKindConfirm {
		k.writeConfirmBody(&builder, data)
	} else {
		k.writeUpdateBody(&builder, data)
	}

	builder.WriteString(`</div></body></html>`)

	return builder.String()
}

func (k MailKind) writeConfirmBody(builder *strings.Builder, data *MailData) {
	fmt.Fprint(builder, `<h1 style="font-size:22px;color:#1a1a2e;">Confirm your subscription</h1>`)
	fmt.Fprintf(builder, `<p>You asked to receive status updates for <strong>%s</strong>. `,
		html.EscapeString(data.PageName))
	fmt.Fprint(builder, `Please confirm your email address to start receiving notifications.</p>`)
	fmt.Fprintf(builder, `<p style="padding:8px 0;"><a href="%s" `+
		`style="display:inline-block;padding:12px 28px;background:#16a34a;color:#fff;`+
		`text-decoration:none;border-radius:6px;font-weight:600;">Confirm subscription</a></p>`,
		html.EscapeString(data.ConfirmURL))
	fmt.Fprint(builder, `<p style="font-size:13px;color:#4a5568;">Or paste this link into your browser:</p>`)
	fmt.Fprintf(builder, `<p style="font-size:13px;word-break:break-all;color:#4a5568;">%s</p>`,
		html.EscapeString(data.ConfirmURL))
	fmt.Fprint(builder, `<p style="font-size:12px;color:#718096;margin-top:24px;">`+
		`If you didn't request this, you can safely ignore this email.</p>`)
}

func (k MailKind) writeUpdateBody(builder *strings.Builder, data *MailData) {
	heading := data.Title
	fmt.Fprintf(builder, `<p style="font-size:13px;color:#718096;text-transform:uppercase;`+
		`letter-spacing:0.04em;margin:0 0 4px;">%s</p>`, html.EscapeString(k.label()))
	fmt.Fprintf(builder, `<h1 style="font-size:22px;color:#1a1a2e;margin:0 0 16px;">%s</h1>`,
		html.EscapeString(heading))

	if data.BodyMarkdown != "" {
		fmt.Fprintf(builder, `<div style="white-space:pre-wrap;">%s</div>`,
			html.EscapeString(data.BodyMarkdown))
	}

	if data.LinkURL != "" {
		fmt.Fprintf(builder, `<p style="padding:8px 0;"><a href="%s" `+
			`style="color:#2563eb;">More information</a></p>`, html.EscapeString(data.LinkURL))
	}

	fmt.Fprint(builder, `<hr style="border:none;border-top:1px solid #edf2f7;margin:24px 0;">`)
	fmt.Fprintf(builder, `<p style="font-size:12px;color:#718096;">`+
		`You're receiving this because you subscribed to %s. `, html.EscapeString(data.PageName))
	fmt.Fprintf(builder, `<a href="%s" style="color:#4a5568;">Unsubscribe</a>.</p>`,
		html.EscapeString(data.UnsubscribeURL))
}

// buildText renders the plaintext body for a message kind.
func (k MailKind) buildText(data *MailData) string {
	var builder strings.Builder

	if k == MailKindConfirm {
		fmt.Fprintf(&builder, "Confirm your subscription to %s\n\n", data.PageName)
		builder.WriteString("You asked to receive status updates. Confirm your email address:\n\n")
		builder.WriteString(data.ConfirmURL + "\n\n")
		builder.WriteString("If you didn't request this, you can safely ignore this email.\n")

		return builder.String()
	}

	fmt.Fprintf(&builder, "%s\n\n", k.label())
	fmt.Fprintf(&builder, "%s\n\n", data.Title)

	if data.BodyMarkdown != "" {
		builder.WriteString(data.BodyMarkdown + "\n\n")
	}

	if data.LinkURL != "" {
		fmt.Fprintf(&builder, "More information: %s\n\n", data.LinkURL)
	}

	fmt.Fprintf(&builder, "You're receiving this because you subscribed to %s.\n", data.PageName)
	fmt.Fprintf(&builder, "Unsubscribe: %s\n", data.UnsubscribeURL)

	return builder.String()
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
