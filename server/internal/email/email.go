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
	// ListUnsubscribeURL, when non-empty, sets the RFC 2369 List-Unsubscribe
	// header to <URL> — the per-recipient unsubscribe link (spec
	// 2026-07-05-10, D4). Only incident/alert emails set this; transactional
	// emails (registration, reset, invitation, password-changed) leave it
	// empty and carry no List-Unsubscribe headers at all (spec acceptance
	// criterion 6).
	ListUnsubscribeURL string
	// ListUnsubscribePostOneClick, when true alongside a non-empty
	// ListUnsubscribeURL, sets List-Unsubscribe-Post: List-Unsubscribe=One-Click
	// (RFC 8058) — tells compliant mail clients (Gmail, Apple Mail, etc.) to
	// offer a one-click unsubscribe button that POSTs to ListUnsubscribeURL
	// with no user-visible page.
	ListUnsubscribePostOneClick bool
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
	// has none), the HTML body with inlined CSS, and a plaintext alternative
	// (from a {{define "text"}} block, or "" when the template has none).
	//
	// Templates without a "text" block (e.g. auth templates that have not
	// been extended yet) simply produce text == "" — callers should treat an
	// empty text as "no plaintext alternative" rather than an error. The
	// auto-generated lynx-style rendering of our wrapper tables is
	// unreadable in real clients, so an explicit template-authored "text"
	// block is required to get a plaintext part; there is no automatic
	// HTML-to-text fallback.
	Format(templateName string, data any) (subject string, html string, text string, err error)
}
