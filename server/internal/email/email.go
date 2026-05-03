// Package email provides email formatting and sending capabilities.
package email

import "context"

// Recipients holds email recipient addresses.
type Recipients struct {
	To      []string // Primary recipients (required)
	CC      []string // Carbon copy recipients (optional)
	BCC     []string // Blind carbon copy recipients (optional)
	ReplyTo string   // Reply-to address (optional, defaults to From)
}

// Message represents an email to send.
type Message struct {
	Recipients Recipients // Email recipients
	Subject    string     // Email subject
	HTML       string     // HTML body
	Text       string     // Plain text body (fallback)
}

// Sender handles email delivery.
type Sender interface {
	// Send delivers an email. Returns SendResult with delivery status.
	// Returns nil result and nil error if email is disabled (no-op).
	// Returns error if sending fails.
	Send(ctx context.Context, msg *Message) (*SendResult, error)
}

// Formatter renders email templates.
type Formatter interface {
	// Format renders a template with the given data and returns the rendered
	// subject (from a {{define "subject"}} block, or "" when the template
	// has none) and the HTML body with inlined CSS.
	//
	// No plaintext fallback is produced — the auto-generated lynx-style
	// rendering of our wrapper tables is unreadable in real clients, and
	// HTML-only is sufficient for transactional mail. Callers that have a
	// hand-written plaintext alternative can supply it via Message.Text
	// directly.
	Format(templateName string, data any) (subject string, html string, err error)
}
