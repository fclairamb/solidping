package email

import (
	"io/fs"
	"strings"
)

// SupportReplyNoticeText is the plain-text form of the "you can reply to this
// email" notice (spec 2026-08-22-02). It must appear in the TEXT body as well
// as the HTML one, or it vanishes for text-only clients.
//
// The address itself is deliberately NOT in the body: the Reply-To header
// carries it, and a literal address in the copy drifts the moment the config
// changes.
const SupportReplyNoticeText = "You can reply directly to this email to reach a human — we read every reply."

// supportReplyNoticeHTML is the HTML form of the same notice. Inline styles
// only: the formatter has already run its CSS inliner by the time this is
// spliced in, so a class would render unstyled.
const supportReplyNoticeHTML = `<div style="margin:16px auto 0;max-width:600px;padding:0 24px;` +
	`font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;` +
	`font-size:13px;line-height:1.5;color:#6b7280;text-align:center;">` +
	`&#8505;&#65039; You can reply directly to this email to reach a human — we read every reply.` +
	`</div>`

// supportReplyableTemplates is the EXPLICIT classification of every shipped
// email template: may it carry the instance support Reply-To and the "you can
// reply" notice?
//
// `false` here is not the same as "absent". Absent is a BUILD FAILURE:
// TestSupportReplyableClassification walks the embedded templates directory and
// requires every template to appear in this map, so a new template cannot be
// added without someone deciding, deliberately, which side of the line it sits
// on.
//
// The line itself is not new — it is the same partition the repo already draws
// for List-Unsubscribe ("transactional emails: registration, reset, invitation,
// password-changed"), reused rather than re-invented. Security-critical mail
// keeps NO Reply-To because a Reply-To pointing at a human mailbox invites the
// recipient to answer a password-reset mail — plausibly pasting a credential, a
// reset link or a code into an email to a person — and undermines the "this is
// automated, do not engage" framing anti-phishing guidance leans on for exactly
// these messages.
//
//nolint:gochecknoglobals // package-level classification table
var supportReplyableTemplates = map[string]bool{
	// Alerts and notifications — a human replying to these is the whole point.
	"escalation.html":                  true,
	"incident-burn-created.html":       true,
	"incident-burn-resolved.html":      true,
	"incident-comment.html":            true,
	"incident-created.html":            true,
	"incident-escalated.html":          true,
	"incident-reopened.html":           true,
	"incident-resolved.html":           true,
	"membership_request_decision.html": true,
	"membership_request_new.html":      true,
	"paging-nudge.html":                true,
	"status-subscriber-update.html":    true,
	"test-email.html":                  true,
	"uptime-report.html":               true,
	"welcome.html":                     true,

	// Security-critical / identity mail — never carries a reply path.
	"invitation.html":                false,
	"password-changed.html":          false,
	"password-reset.html":            false,
	"registration.html":              false,
	"status-subscriber-confirm.html": false,
}

// SupportReplyable reports whether a rendered template may carry the instance
// support Reply-To. Unknown templates return false — fail closed, so an
// unclassified template is silently safe rather than silently leaking a reply
// path. The raw-HTML job path (no template name at all) lands here too.
func SupportReplyable(templateName string) bool {
	return supportReplyableTemplates[templateName]
}

// classifiedTemplates returns a copy of the classification table. Used by the
// enumerating test; not part of the runtime path.
func classifiedTemplates() map[string]bool {
	out := make(map[string]bool, len(supportReplyableTemplates))
	for name, replyable := range supportReplyableTemplates {
		out[name] = replyable
	}

	return out
}

// shippedTemplateNames lists every template file shipped in the embedded
// templates directory, excluding the base wrapper (which is never rendered on
// its own and therefore has no classification of its own).
func shippedTemplateNames() ([]string, error) {
	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(entries))

	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "base.html" {
			continue
		}

		names = append(names, entry.Name())
	}

	return names, nil
}

// applySupportReply stamps the instance support Reply-To and its notice onto a
// message that opted in, and does nothing at all otherwise.
//
// It runs at the SENDER layer rather than inside base.html on purpose. The
// notice has to reach both bodies, and the plain-text body is hand-authored per
// template in a {{define "text"}} block — there is no shared text wrapper to
// hook. Threading a new field through base.html would additionally mean adding
// it to every view model, because html/template errors on a missing STRUCT
// field and several view models are structs. One splice point, covered by
// tests, beats twenty-one template edits plus a field on every view model.
//
// An explicit per-message ReplyTo always wins: the instance value is a DEFAULT,
// never an override.
func applySupportReply(msg *Message, defaultReplyTo string) {
	if !msg.SupportReplyable || defaultReplyTo == "" {
		return
	}

	if msg.Recipients.ReplyTo == "" {
		msg.Recipients.ReplyTo = defaultReplyTo
	}

	msg.Text = appendTextNotice(msg.Text)
	msg.HTML = injectHTMLNotice(msg.HTML)
}

// appendTextNotice adds the notice to the plain-text body, idempotently.
func appendTextNotice(text string) string {
	if text == "" || strings.Contains(text, SupportReplyNoticeText) {
		return text
	}

	return strings.TrimRight(text, "\n") + "\n\n" + SupportReplyNoticeText + "\n"
}

// injectHTMLNotice splices the notice into the HTML body just before the
// closing </body>, falling back to an append when the body is a fragment rather
// than a document.
func injectHTMLNotice(html string) string {
	if html == "" || strings.Contains(html, "You can reply directly to this email") {
		return html
	}

	if idx := strings.LastIndex(strings.ToLower(html), "</body>"); idx >= 0 {
		return html[:idx] + supportReplyNoticeHTML + html[idx:]
	}

	return html + supportReplyNoticeHTML
}
