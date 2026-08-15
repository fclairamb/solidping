package telegram

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// IncidentRef renders the short per-org reference as humans see it everywhere:
// "#42". Declared once so the dashboard, Slack and Telegram cannot drift into
// three spellings of the same identifier.
func IncidentRef(number int64) string {
	return "#" + strconv.FormatInt(number, 10)
}

// QualifiedIncidentRef renders the ORG-QUALIFIED reference, "#acme:42".
//
// Incident numbers are per-org sequential, so "#42" exists in every
// organization. In a chat linked to several orgs the short form is genuinely
// ambiguous — both to the human reading the alert and to the bot parsing the
// `/ack 42` they type back — so such a chat gets the qualified form everywhere,
// rendered AND accepted (see ParseIncidentRef).
//
// An empty slug degrades to the short form rather than rendering "#:42": a
// missing slug is a cosmetic lookup failure, never a reason to emit a reference
// nothing can parse.
func QualifiedIncidentRef(orgSlug string, number int64) string {
	slug := strings.TrimSpace(orgSlug)
	if slug == "" {
		return IncidentRef(number)
	}

	return "#" + slug + ":" + strconv.FormatInt(number, 10)
}

// incidentRefOf is the single decision point for "short or qualified". Every
// renderer goes through it, so a multi-org chat cannot end up with one message
// qualifying its reference and the next one not.
func incidentRefOf(qualify bool, orgSlug string, number int64) string {
	if qualify {
		return QualifiedIncidentRef(orgSlug, number)
	}

	return IncidentRef(number)
}

// AckCallbackData is the callback_data of an Acknowledge button.
func AckCallbackData(incidentUID string) string {
	return CallbackActionAck + ":" + incidentUID
}

// IncidentKeyboard is the combined inline keyboard attached to anything that
// can show an incident: `[✅ Acknowledge][🔎 View]` when both are available,
// down to a single button when only one is, down to nil when neither is —
// callers can pass the result straight through to Message.ReplyMarkup.
//
// The View button carries a `url`, never a `callback_data`: opening a link
// needs no verb, no ParseCallbackData change, no dispatch change, and Telegram
// opens it client-side without ever hitting the bot's webhook.
func IncidentKeyboard(incidentUID, incidentURL string, canAck bool) *InlineKeyboard {
	buttons := make([]InlineButton, 0, 2)

	if canAck && strings.TrimSpace(incidentUID) != "" {
		buttons = append(buttons, InlineButton{
			Text:         "✅ Acknowledge",
			CallbackData: AckCallbackData(incidentUID),
		})
	}

	if url := strings.TrimSpace(incidentURL); url != "" {
		buttons = append(buttons, InlineButton{Text: "🔎 View", URL: url})
	}

	if len(buttons) == 0 {
		return nil
	}

	return NewInlineKeyboard(buttons...)
}

// IncidentKeyboardForEdit is IncidentKeyboard for an EDIT rather than a new
// send. It never returns nil: Telegram treats a nil ReplyMarkup on an edit as
// "leave the existing buttons in place" (see client.go's Edit.ReplyMarkup
// doc), so when there is nothing left to show — no ack, no URL — this falls
// back to the EmptyInlineKeyboard() removal marker instead, which is the only
// way to actually take a stale Acknowledge button away.
func IncidentKeyboardForEdit(incidentUID, incidentURL string, canAck bool) *InlineKeyboard {
	if keyboard := IncidentKeyboard(incidentUID, incidentURL, canAck); keyboard != nil {
		return keyboard
	}

	return EmptyInlineKeyboard()
}

// IncidentURL builds the dashboard deep link for an incident, or "" when
// baseURL or orgSlug is missing — callers then simply ship without a link
// rather than a broken one. The one builder every Telegram surface shares, so
// the escalation alert path, the callback edit path and the /incidents,
// /incident replies cannot drift into disagreeing URL shapes.
func IncidentURL(baseURL, orgSlug, incidentUID string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" || orgSlug == "" {
		return ""
	}

	return trimmed + "/dash0/orgs/" + orgSlug + "/incidents/" + incidentUID
}

// FormatOpenFor renders how long an incident has been open, in the compact form
// an on-call person reads at a glance ("23m", "3h12m", "2d4h").
func FormatOpenFor(elapsed time.Duration) string {
	if elapsed < 0 {
		elapsed = 0
	}

	switch {
	case elapsed < time.Minute:
		return fmt.Sprintf("%ds", int(elapsed.Seconds()))
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(elapsed.Hours()), int(elapsed.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(elapsed.Hours())/24, int(elapsed.Hours())%24)
	}
}

// BuildAcknowledgedHTML is the replacement body written over the alert once its
// Acknowledge button is pressed.
//
// The EDIT is the point, not the toast: a toast is gone in two seconds and only
// the presser ever sees it, whereas the rewritten message is what the next
// person scrolling the chat reads. An alert still showing a live Acknowledge
// button after someone took the page is how two people end up debugging the
// same outage.
func BuildAcknowledgedHTML(params *AlertParams, who string, ackedAt time.Time) string {
	acked := *params

	var body strings.Builder

	body.WriteString(buildAlertHTML(&acked, "✅ "))
	body.WriteString("\n\n<b>Acknowledged by ")
	body.WriteString(EscapeHTML(strings.TrimSpace(who)))
	body.WriteString("</b> at ")
	body.WriteString(EscapeHTML(ackedAt.UTC().Format("2006-01-02 15:04 UTC")))

	return body.String()
}

// StatusView is the input to BuildStatusHTML.
type StatusView struct {
	OrgSlug string
	// TotalChecks is every enabled check in the org.
	TotalChecks int
	// ChecksDown is how many of them are currently inside an open incident.
	ChecksDown int
	// OpenIncidents is the count of open, non-suppressed incidents.
	OpenIncidents int
}

// BuildStatusHTML renders the one-line org health answer to /status.
func BuildStatusHTML(view *StatusView) string {
	org := EscapeHTML(strings.TrimSpace(view.OrgSlug))

	if view.OpenIncidents == 0 {
		return fmt.Sprintf("✅ <b>%s</b> — all %d checks up.", org, view.TotalChecks)
	}

	checksUp := view.TotalChecks - view.ChecksDown
	if checksUp < 0 {
		checksUp = 0
	}

	return fmt.Sprintf("🔥 <b>%s</b> — %s open, %d/%d checks up.\nSend /incidents for the list.",
		org, pluralize(view.OpenIncidents, "incident"), checksUp, view.TotalChecks)
}

// IncidentLine is one row of the /incidents listing.
type IncidentLine struct {
	Number int64
	UID    string
	// OrgSlug is the organization the incident belongs to; it only reaches the
	// rendered row when QualifyRef is set.
	OrgSlug string
	// QualifyRef renders "#org:42" instead of "#42" — set when the destination
	// chat is linked in more than one organization.
	QualifyRef bool
	CheckName  string
	State      string
	OpenFor    time.Duration
	Acked      bool
	AckedBy    string
	// IncidentURL is the dashboard deep link, rendered as the View button
	// rather than inline text — never both, so the row does not say it twice.
	IncidentURL string
}

// Ref renders this row's reference in whichever form the destination chat needs.
func (l *IncidentLine) Ref() string {
	return incidentRefOf(l.QualifyRef, l.OrgSlug, l.Number)
}

// BuildIncidentsHTML renders the /incidents listing header. Each incident then
// ships as its OWN message so it can carry its own Acknowledge button —
// Telegram binds one inline keyboard per message, so a single combined message
// could only ever ack one of them.
func BuildIncidentsHTML(orgSlug string, count int) string {
	return fmt.Sprintf("🔥 <b>%s</b> — %s open:",
		EscapeHTML(strings.TrimSpace(orgSlug)), pluralize(count, "incident"))
}

// BuildIncidentLineHTML renders one listing row.
func BuildIncidentLineHTML(line *IncidentLine) string {
	var body strings.Builder

	body.WriteString("<b>")
	body.WriteString(EscapeHTML(line.Ref()))
	body.WriteString("</b> ")
	body.WriteString(EscapeHTML(checkNameOr(line.CheckName)))
	body.WriteString(" — ")
	body.WriteString(EscapeHTML(stateOr(line.State)))
	body.WriteString(", open ")
	body.WriteString(FormatOpenFor(line.OpenFor))

	if line.Acked {
		body.WriteString("\n✅ acknowledged")

		if who := strings.TrimSpace(line.AckedBy); who != "" {
			body.WriteString(" by ")
			body.WriteString(EscapeHTML(who))
		}
	}

	return body.String()
}

// BuildNoOpenIncidentsHTML is the /incidents answer when nothing is broken.
func BuildNoOpenIncidentsHTML(orgSlug string) string {
	return fmt.Sprintf("✅ <b>%s</b> — no open incidents.",
		EscapeHTML(strings.TrimSpace(orgSlug)))
}

// IncidentDetailView is the input to BuildIncidentDetailHTML.
type IncidentDetailView struct {
	Number int64
	// OrgSlug / QualifyRef carry the same meaning as on IncidentLine.
	OrgSlug    string
	QualifyRef bool
	CheckName  string
	State      string
	OpenFor    time.Duration
	Regions    []string
	LastError  string
	AckedBy    string
	AckedAt    *time.Time
	ResolvedAt *time.Time
	// IncidentURL is the dashboard deep link, rendered as the View button
	// rather than inline text — never both, so the reply does not say it twice.
	IncidentURL string
}

// Ref renders this view's reference in whichever form the destination chat
// needs.
func (v *IncidentDetailView) Ref() string {
	return incidentRefOf(v.QualifyRef, v.OrgSlug, v.Number)
}

// BuildIncidentDetailHTML renders the /incident <#ref> answer.
func BuildIncidentDetailHTML(view *IncidentDetailView) string {
	var body strings.Builder

	body.WriteString("<b>")
	body.WriteString(StateEmoji(stateOr(view.State)))
	body.WriteString(" ")
	body.WriteString(EscapeHTML(view.Ref()))
	body.WriteString(" — ")
	body.WriteString(EscapeHTML(checkNameOr(view.CheckName)))
	body.WriteString("</b>\n\n")

	body.WriteString("<b>Status:</b> ")
	body.WriteString(EscapeHTML(stateOr(view.State)))
	body.WriteString("\n")

	body.WriteString("<b>Duration:</b> ")
	body.WriteString(FormatOpenFor(view.OpenFor))
	body.WriteString("\n")

	if regions := joinNonEmpty(view.Regions); regions != "" {
		body.WriteString("<b>Failing regions:</b> ")
		body.WriteString(EscapeHTML(regions))
		body.WriteString("\n")
	}

	if err := strings.TrimSpace(view.LastError); err != "" {
		body.WriteString("<b>Last error:</b> ")
		body.WriteString(EscapeHTML(truncate(err, detailErrorCap)))
		body.WriteString("\n")
	}

	writeAckLine(&body, view)

	return strings.TrimRight(body.String(), "\n")
}

// detailErrorCap bounds the last-error line. A stack trace pasted into a chat
// is unreadable on a phone and can push the message past Telegram's 4096-char
// ceiling, which would drop the whole reply.
const detailErrorCap = 300

// writeAckLine appends the acknowledgement / resolution attribution.
func writeAckLine(body *strings.Builder, view *IncidentDetailView) {
	if view.AckedAt != nil {
		body.WriteString("<b>Acknowledged:</b> ")

		if who := strings.TrimSpace(view.AckedBy); who != "" {
			body.WriteString(EscapeHTML(who))
			body.WriteString(" at ")
		}

		body.WriteString(EscapeHTML(view.AckedAt.UTC().Format("2006-01-02 15:04 UTC")))
		body.WriteString("\n")

		return
	}

	if view.ResolvedAt == nil {
		body.WriteString("<b>Acknowledged:</b> not yet\n")
	}
}

// BuildAckedHTML confirms a typed /ack.
//
// ref is a RENDERED reference (short or org-qualified) rather than a bare
// number: in a multi-org chat the confirmation has to name which org's #42 was
// just taken, or it confirms nothing.
func BuildAckedHTML(ref, checkName string) string {
	return fmt.Sprintf("✅ <b>%s</b> acknowledged — %s.",
		EscapeHTML(ref), EscapeHTML(checkNameOr(checkName)))
}

// BuildAlreadyAckedHTML is the idempotent answer: acking twice is a no-op that
// reports the state rather than an error. Two people pressing the same button
// within a second of each other is the normal case during an outage, not an
// exception.
func BuildAlreadyAckedHTML(ref, who string, ackedAt time.Time) string {
	if who = strings.TrimSpace(who); who != "" {
		return fmt.Sprintf("✅ %s was already acknowledged by %s at %s.",
			EscapeHTML(ref), EscapeHTML(who), ackedAt.UTC().Format("15:04 UTC"))
	}

	return fmt.Sprintf("✅ %s was already acknowledged at %s.",
		EscapeHTML(ref), ackedAt.UTC().Format("15:04 UTC"))
}

// BuildIncidentResolvedHTML is the answer to acking a closed incident.
func BuildIncidentResolvedHTML(ref string) string {
	return fmt.Sprintf("🟢 %s is already resolved — nothing to acknowledge.", EscapeHTML(ref))
}

// BuildNotConnectedToOrgHTML is the answer to a reference qualified with an org
// slug this chat is not linked to.
//
// It deliberately does NOT say whether that organization exists: the only thing
// it reveals is which orgs the chat itself is connected to, which its own user
// already knows.
func BuildNotConnectedToOrgHTML(orgSlug string) string {
	return fmt.Sprintf(
		"This chat isn't connected to <b>%s</b>. "+
			"Use the connect link from that organization's dashboard (Account → Notifications).",
		EscapeHTML(strings.TrimSpace(orgSlug)))
}

// BuildAmbiguousRefHTML is the answer to a BARE reference in a chat linked to
// several organizations, when the number exists in more than one of them.
//
// Guessing is the one thing that must never happen here: acking the wrong org's
// #42 silences a page nobody was looking at while the real outage stays
// unclaimed, and it does so silently.
func BuildAmbiguousRefHTML(command string, lines []IncidentLine) string {
	var body strings.Builder

	body.WriteString("That number exists in more than one of your organizations:\n")

	for i := range lines {
		body.WriteString("\n• <b>")
		body.WriteString(EscapeHTML(QualifiedIncidentRef(lines[i].OrgSlug, lines[i].Number)))
		body.WriteString("</b> — ")
		body.WriteString(EscapeHTML(checkNameOr(lines[i].CheckName)))
		body.WriteString(" (")
		body.WriteString(EscapeHTML(stateOr(lines[i].State)))
		body.WriteString(" ")
		body.WriteString(FormatOpenFor(lines[i].OpenFor))
		body.WriteString(")")
	}

	body.WriteString("\n\nSend <code>/")
	body.WriteString(EscapeHTML(command))
	body.WriteString(" ")

	if len(lines) > 0 {
		body.WriteString(EscapeHTML(QualifiedIncidentRef(lines[0].OrgSlug, lines[0].Number)))
	} else {
		body.WriteString("#org:42")
	}

	body.WriteString("</code> with the one you mean.")

	return body.String()
}

// BuildIncidentNotFoundHTML is the answer to a reference nothing matches.
func BuildIncidentNotFoundHTML(ref string) string {
	return fmt.Sprintf("No incident <b>%s</b> in this organization. Send /incidents to see what is open.",
		EscapeHTML(strings.TrimSpace(ref)))
}

// BuildAckNeedsRefHTML lists the candidates when a bare /ack is ambiguous.
// Guessing would ack the wrong incident, which is silently worse than asking.
func BuildAckNeedsRefHTML(lines []IncidentLine) string {
	var body strings.Builder

	body.WriteString("Which one? Several incidents are open:\n")

	for i := range lines {
		body.WriteString("\n• <b>")
		body.WriteString(EscapeHTML(lines[i].Ref()))
		body.WriteString("</b> ")
		body.WriteString(EscapeHTML(checkNameOr(lines[i].CheckName)))
	}

	body.WriteString("\n\nSend <code>/ack ")

	if len(lines) > 0 {
		body.WriteString(EscapeHTML(lines[0].Ref()))
	} else {
		body.WriteString("#42")
	}

	body.WriteString("</code> with the one you mean.")

	return body.String()
}

// BuildNothingToAckHTML is the bare-/ack answer when nothing is open.
func BuildNothingToAckHTML() string {
	return "✅ Nothing to acknowledge — no open incidents."
}

// BuildBadRefHTML is the answer to an unparseable reference.
func BuildBadRefHTML(command string) string {
	return fmt.Sprintf("I need an incident number, like <code>/%s #42</code>.", EscapeHTML(command))
}

// ActorLabel renders who actually acted from Telegram: "via Telegram (Alice)".
//
// A Telegram ack is CREDITED to the SolidPing account the chat is linked to,
// because that is the only identity the platform knows. In a group the presser
// is frequently somebody else, and the user UID alone cannot say so — this
// label is what keeps the timeline honest about it.
func ActorLabel(firstName string) string {
	if name := strings.TrimSpace(firstName); name != "" {
		return "via Telegram (" + name + ")"
	}

	return "via Telegram"
}

// BuildAckFailedHTML is the answer when the acknowledgement itself failed.
func BuildAckFailedHTML() string {
	return "Could not acknowledge that incident — please try again, or use the dashboard."
}

// BuildNotLinkedHTML is the reply to a command from a chat bound to no
// SolidPing account.
//
// Deliberately identical for every command and every chat: an unlinked chat
// must not be able to learn whether an org, an incident or a check exists.
func BuildNotLinkedHTML() string {
	return "<b>This chat is not connected</b>\n\n" +
		"Link your SolidPing account first: open your dashboard " +
		"(Account → Notifications) and use the Telegram connect link."
}

// BuildHelpHTML is the command list, and the answer to a bare /start on an
// already-linked chat: someone who re-opens the bot months later types /start
// out of habit and should get something useful, not "this link has expired".
func BuildHelpHTML() string {
	return "<b>SolidPing bot</b>\n\n" +
		"/status — org health in one line\n" +
		"/incidents — the open incidents, each with an Acknowledge button\n" +
		"/ack #42 — acknowledge an incident (no number when only one is open)\n" +
		"/incident #42 — the latest detail on one incident\n" +
		"/help — this message\n" +
		"/stop — disconnect this chat"
}

func checkNameOr(name string) string {
	if trimmed := strings.TrimSpace(name); trimmed != "" {
		return trimmed
	}

	return "check"
}

func stateOr(state string) string {
	if trimmed := strings.TrimSpace(state); trimmed != "" {
		return trimmed
	}

	return StateDown
}

func joinNonEmpty(values []string) string {
	kept := make([]string, 0, len(values))

	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}

	return strings.Join(kept, ", ")
}

func truncate(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= limit {
		return text
	}

	return text[:limit-1] + "…"
}

func pluralize(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}

	return strconv.Itoa(count) + " " + noun + "s"
}
