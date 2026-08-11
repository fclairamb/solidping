package telegram

import "strings"

// Incident state labels rendered into the alert body. One shape covers the
// whole incident lifecycle because the state is a variable, exactly as the
// single WhatsApp template does.
const (
	StateDown      = "DOWN"
	StateEscalated = "ESCALATED"
	StateResolved  = "RESOLVED"
)

// StateEmoji maps an incident state label to its leading emoji. An unknown
// state degrades to the DOWN emoji rather than rendering nothing — an alert
// with no color still has to look like an alert.
func StateEmoji(state string) string {
	switch state {
	case StateResolved:
		return "🟢"
	case StateEscalated:
		return "🟠"
	default:
		return "🔴"
	}
}

// AlertParams are the raw, UNESCAPED values for one incident alert. Everything
// here is attacker-adjacent free text (a check name, an incident title, an org
// slug the user chose), so BuildAlertHTML is the single place that escapes
// them. Callers must never pre-escape: double escaping renders "&amp;amp;".
type AlertParams struct {
	// State is one of StateDown / StateEscalated / StateResolved.
	State string
	// CheckName is the check (or incident) the alert is about.
	CheckName string
	// Detail is the one-line detail: the incident title, or how long it has
	// been open.
	Detail string
	// OrgSlug is the organization the incident belongs to.
	OrgSlug string
	// IncidentURL is the dashboard deep link. Empty omits the link line.
	IncidentURL string
}

// BuildAlertHTML renders one incident alert as Telegram parse_mode=HTML.
//
// EVERY interpolated value passes through EscapeHTML here. Telegram rejects the
// entire message when its entities do not parse, so an unescaped '&' in a check
// name would not merely render oddly — it would silently kill alerting for that
// check. This function is the reason that cannot happen.
func BuildAlertHTML(params *AlertParams) string {
	return buildAlertHTML(params, "")
}

// BuildResolvedOriginalHTML renders the replacement body for the FIRST message
// of an incident once it resolves. Editing the original in place stops someone
// scrolling back through the chat from reading a stale red alert as live.
func BuildResolvedOriginalHTML(params *AlertParams) string {
	resolved := *params
	resolved.State = StateResolved

	return buildAlertHTML(&resolved, "✅ ")
}

// buildAlertHTML is the shared renderer. prefix is prepended to the title line.
func buildAlertHTML(params *AlertParams, prefix string) string {
	state := params.State
	if state == "" {
		state = StateDown
	}

	headline := "Incident"
	if state == StateResolved {
		headline = "Resolved"
	}

	name := strings.TrimSpace(params.CheckName)
	if name == "" {
		name = "check"
	}

	var body strings.Builder

	body.WriteString("<b>")
	body.WriteString(prefix)
	body.WriteString(StateEmoji(state))
	body.WriteString(" ")
	body.WriteString(headline)
	body.WriteString(" — ")
	body.WriteString(EscapeHTML(name))
	body.WriteString("</b>\n\n")

	body.WriteString("<b>Status:</b> ")
	body.WriteString(EscapeHTML(state))
	body.WriteString("\n")

	if detail := strings.TrimSpace(params.Detail); detail != "" {
		body.WriteString("<b>Detail:</b> ")
		body.WriteString(EscapeHTML(detail))
		body.WriteString("\n")
	}

	if org := strings.TrimSpace(params.OrgSlug); org != "" {
		body.WriteString("<b>Org:</b> ")
		body.WriteString(EscapeHTML(org))
		body.WriteString("\n")
	}

	if link := strings.TrimSpace(params.IncidentURL); link != "" {
		// The href is escaped too: a query string carrying '&' is the single
		// most common way to produce an unparseable anchor entity.
		body.WriteString("\n<a href=\"")
		body.WriteString(EscapeHTML(link))
		body.WriteString("\">View incident →</a>")
	}

	return strings.TrimRight(body.String(), "\n")
}

// BuildLinkedHTML is the in-chat confirmation sent right after a chat is bound
// to a SolidPing account. Naming the org matters: a user who connected the
// wrong account notices instantly rather than three weeks later during an
// outage.
func BuildLinkedHTML(orgSlug string) string {
	return "<b>✅ Connected to SolidPing</b>\n\n" +
		"This chat will now receive alerts for the <b>" + EscapeHTML(orgSlug) + "</b> organization.\n" +
		"Send /stop at any time to disconnect."
}

// BuildTestHTML is the message behind the dashboard's "Test" button. It says
// which way the delivery went, because that is the only thing the test proves:
// the chat is connected and the bot can reach it.
func BuildTestHTML() string {
	return "<b>🔔 Test alert from SolidPing</b>\n\n" +
		"This chat is connected and can receive alerts. " +
		"Nothing is wrong — you pressed Test in your dashboard."
}

// BuildUnlinkedHTML confirms an in-chat opt-out.
func BuildUnlinkedHTML() string {
	return "<b>Disconnected</b>\n\n" +
		"This chat will no longer receive SolidPing alerts. " +
		"Generate a new connect link from your dashboard to reconnect."
}

// BuildExpiredLinkHTML is the reply to an unknown or expired connect token.
//
// It deliberately does NOT distinguish "expired" from "never existed": telling
// an anonymous sender which one it was turns the bot into an oracle for probing
// live tokens.
func BuildExpiredLinkHTML() string {
	return "<b>This link has expired</b>\n\n" +
		"Generate a new connect link from your SolidPing dashboard " +
		"(Account → Notifications) and try again."
}

// BuildGroupNotSupportedHTML is the reply to a connect attempt made from a
// group or channel. It names the fix (start a direct chat) rather than simply
// refusing, because from the user's side the link they clicked did work — it
// just landed somewhere this version cannot use.
func BuildGroupNotSupportedHTML() string {
	return "<b>Not supported here</b>\n\n" +
		"SolidPing alerts go to a direct chat, not to a group. " +
		"Open a private chat with this bot and use your connect link again."
}

// BuildUnknownCommandHTML is the reply to a message the bot does not handle. v1
// implements /start and /stop only, and the bot's advertised command list is
// trimmed to match — no command is offered that answers nothing.
func BuildUnknownCommandHTML() string {
	return "SolidPing only understands /start and /stop here. " +
		"Manage your alerts from the dashboard."
}
