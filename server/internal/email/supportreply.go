package email

import (
	"html/template"
	"io/fs"
	"sort"
	"strings"

	"github.com/fclairamb/solidping/server/internal/i18n"
)

// supportReplyNoticeKey is the catalog key of the "you can reply to this
// email" notice (spec 2026-08-22-02), in the reader's language (spec
// 2026-10-21-01). It must appear in the TEXT body as well as the HTML one, or
// it vanishes for text-only clients.
//
// The address itself is deliberately NOT in the body: the Reply-To header
// carries it, and a literal address in the copy drifts the moment the config
// changes.
const supportReplyNoticeKey = "common.supportReply.notice"

// supportReplyNoticeMarker tags the HTML notice so a second pass recognizes it
// whatever language it was written in.
const supportReplyNoticeMarker = `data-solidping-notice="support-reply"`

// SupportReplyNoticeText is the plain-text notice in loc's language.
func SupportReplyNoticeText(loc i18n.Locale) string {
	return loc.T(supportReplyNoticeKey)
}

// supportReplyNoticeHTML is the HTML form of the same notice. Inline styles
// only: the formatter has already run its CSS inliner by the time this is
// spliced in, so a class would render unstyled.
func supportReplyNoticeHTML(loc i18n.Locale) string {
	return `<div ` + supportReplyNoticeMarker + ` style="margin:16px auto 0;max-width:600px;padding:0 24px;` +
		`font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;` +
		`font-size:13px;line-height:1.5;color:#6b7280;text-align:center;">` +
		`&#8505;&#65039; ` + template.HTMLEscapeString(SupportReplyNoticeText(loc)) +
		`</div>`
}

// Template file names. Constants because several of them are referenced from
// more than one call site, and a typo in a classification key would silently
// mean "unclassified" — which fails closed to "no Reply-To" and would be
// invisible without the enumerating test.
const (
	TemplateCustomDomainDemoted     = "custom-domain-demoted.html"
	TemplateEscalation              = "escalation.html"
	TemplateIncidentBurnCreated     = "incident-burn-created.html"
	TemplateIncidentBurnResolved    = "incident-burn-resolved.html"
	TemplateIncidentDegradedCreated = "incident-degraded-created.html"
	TemplateIncidentDegradedResolv  = "incident-degraded-resolved.html"
	TemplateEmailChanged            = "email-changed.html"
	TemplateDeviceTokenCreated      = "device-token-created.html"
	TemplatePasskeyAdded            = "passkey-added.html"
	TemplateIncidentAcknowledged    = "incident-acknowledged.html"
	TemplateIncidentUnacknowledged  = "incident-unacknowledged.html"
	TemplateIncidentComment         = "incident-comment.html"
	TemplateIncidentCreated         = "incident-created.html"
	TemplateIncidentEscalated       = "incident-escalated.html"
	TemplateIncidentReopened        = "incident-reopened.html"
	TemplateIncidentReminder        = "incident-reminder.html"
	TemplateIncidentResolved        = "incident-resolved.html"
	TemplateInvitation              = "invitation.html"
	TemplateMembershipReqDecision   = "membership-request-decision.html"
	TemplateMembershipReqNew        = "membership-request-new.html"
	TemplatePagingNudge             = "paging-nudge.html"
	TemplatePasswordChanged         = "password-changed.html"
	TemplatePasswordReset           = "password-reset.html"
	TemplatePasswordResetSSO        = "password-reset-sso.html"
	TemplateRegionOffline           = "region-offline.html"
	TemplateRegionRecovered         = "region-recovered.html"
	TemplateRegistration            = "registration.html"
	TemplateStatusSubscriberConfirm = "status-subscriber-confirm.html"
	TemplateStatusSubscriberUpdate  = "status-subscriber-update.html"
	TemplateTestEmail               = "test-email.html"
	TemplateUptimeReport            = "uptime-report.html"
	TemplateUsageCapReached         = "usage-cap-reached.html"
	TemplateWelcome                 = "welcome.html"
	// TemplateBase is the shared wrapper. It is never rendered on its own and
	// therefore carries no classification.
	TemplateBase = "base.html"
)

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
	TemplateCustomDomainDemoted: true,
	// An admin told their plan ran out of SMS/voice/WhatsApp alerts may well
	// want to ask about it.
	TemplateUsageCapReached:      true,
	TemplateEscalation:           true,
	TemplateIncidentBurnCreated:  true,
	TemplateIncidentBurnResolved: true,
	// A degraded notice is the most answerable alert of all: it says "this has
	// been flaky for forty minutes and is up right now", which is a conversation,
	// not a page (spec 2026-09-22-03).
	TemplateIncidentDegradedCreated: true,
	TemplateIncidentDegradedResolv:  true,
	TemplateIncidentAcknowledged:    true,
	// The retraction is the most answerable alert there is: it says an open
	// incident is unowned again and asks someone to take it.
	TemplateIncidentUnacknowledged: true,
	TemplateIncidentComment:        true,
	TemplateIncidentCreated:        true,
	TemplateIncidentEscalated:      true,
	TemplateIncidentReopened:       true,
	TemplateIncidentReminder:       true,
	TemplateIncidentResolved:       true,
	TemplateMembershipReqDecision:  true,
	TemplateMembershipReqNew:       true,
	TemplatePagingNudge:            true,
	// A region outage notice (spec 2026-09-25-03) is the platform telling a
	// customer it could not watch their checks: exactly the mail someone
	// answers with a question.
	TemplateRegionOffline:          true,
	TemplateRegionRecovered:        true,
	TemplateStatusSubscriberUpdate: true,
	TemplateTestEmail:              true,
	TemplateUptimeReport:           true,
	TemplateWelcome:                true,

	// Security-critical / identity mail — never carries a reply path.
	TemplateInvitation:              false,
	TemplatePasswordChanged:         false,
	TemplateEmailChanged:            false,
	TemplateDeviceTokenCreated:      false,
	TemplatePasskeyAdded:            false,
	TemplatePasswordReset:           false,
	TemplatePasswordResetSSO:        false,
	TemplateRegistration:            false,
	TemplateStatusSubscriberConfirm: false,
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
	return ShippedTemplateNames()
}

// ShippedTemplateNames lists every template file shipped in the embedded
// templates directory, sorted, excluding base.html.
//
// Exported so other packages enumerate the real directory instead of keeping a
// hand-maintained copy of the list: the email preview package's fixture-coverage
// test reads this, which is what makes "a new template ships without a preview
// fixture" a failing test rather than a silent gap.
func ShippedTemplateNames() ([]string, error) {
	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(entries))

	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == TemplateBase {
			continue
		}

		names = append(names, entry.Name())
	}

	sort.Strings(names)

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

	loc := i18n.New(msg.Locale, msg.Timezone)
	msg.Text = appendTextNotice(msg.Text, loc)
	msg.HTML = injectHTMLNotice(msg.HTML, loc)
}

// appendTextNotice adds the notice to the plain-text body, idempotently: a
// body that already carries it, in any language, is left alone.
func appendTextNotice(text string, loc i18n.Locale) string {
	if text == "" {
		return text
	}

	for _, lang := range i18n.Supported() {
		if strings.Contains(text, SupportReplyNoticeText(i18n.New(lang, ""))) {
			return text
		}
	}

	return strings.TrimRight(text, "\n") + "\n\n" + SupportReplyNoticeText(loc) + "\n"
}

// injectHTMLNotice splices the notice into the HTML body just before the
// closing </body>, falling back to an append when the body is a fragment rather
// than a document. Idempotent through the marker attribute, which no language
// changes.
func injectHTMLNotice(html string, loc i18n.Locale) string {
	if html == "" || strings.Contains(html, supportReplyNoticeMarker) {
		return html
	}

	notice := supportReplyNoticeHTML(loc)

	if idx := strings.LastIndex(strings.ToLower(html), "</body>"); idx >= 0 {
		return html[:idx] + notice + html[idx:]
	}

	return html + notice
}
