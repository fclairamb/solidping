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
	// Format renders a template with the given data.
	// Returns both HTML (with inlined CSS) and plain text versions.
	Format(templateName string, data any) (html string, text string, err error)
}
