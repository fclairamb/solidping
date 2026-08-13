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

// AckCallbackData is the callback_data of an Acknowledge button.
func AckCallbackData(incidentUID string) string {
	return CallbackActionAck + ":" + incidentUID
}

// AckKeyboard is the one-button inline keyboard attached to an open, unacked
// alert. Returns nil when there is no incident to ack, so callers can pass the
// result straight through to Message.ReplyMarkup.
func AckKeyboard(incidentUID string) *InlineKeyboard {
	if strings.TrimSpace(incidentUID) == "" {
		return nil
	}

	return NewInlineKeyboard(InlineButton{
		Text:         "✅ Acknowledge",
		CallbackData: AckCallbackData(incidentUID),
	})
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
	Number      int64
	UID         string
	CheckName   string
	State       string
	OpenFor     time.Duration
	Acked       bool
	AckedBy     string
	IncidentURL string
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
	body.WriteString(IncidentRef(line.Number))
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

	if link := strings.TrimSpace(line.IncidentURL); link != "" {
		body.WriteString("\n<a href=\"")
		body.WriteString(EscapeHTML(link))
		body.WriteString("\">View incident →</a>")
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
	Number      int64
	CheckName   string
	State       string
	OpenFor     time.Duration
	Regions     []string
	LastError   string
	AckedBy     string
	AckedAt     *time.Time
	ResolvedAt  *time.Time
	IncidentURL string
}

// BuildIncidentDetailHTML renders the /incident <#ref> answer.
func BuildIncidentDetailHTML(view *IncidentDetailView) string {
	var body strings.Builder

	body.WriteString("<b>")
	body.WriteString(StateEmoji(stateOr(view.State)))
	body.WriteString(" ")
	body.WriteString(IncidentRef(view.Number))
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

	if link := strings.TrimSpace(view.IncidentURL); link != "" {
		body.WriteString("\n<a href=\"")
		body.WriteString(EscapeHTML(link))
		body.WriteString("\">View incident →</a>")
	}

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
func BuildAckedHTML(number int64, checkName string) string {
	return fmt.Sprintf("✅ <b>%s</b> acknowledged — %s.",
		IncidentRef(number), EscapeHTML(checkNameOr(checkName)))
}

// BuildAlreadyAckedHTML is the idempotent answer: acking twice is a no-op that
// reports the state rather than an error. Two people pressing the same button
// within a second of each other is the normal case during an outage, not an
// exception.
func BuildAlreadyAckedHTML(number int64, who string, ackedAt time.Time) string {
	if who = strings.TrimSpace(who); who != "" {
		return fmt.Sprintf("✅ %s was already acknowledged by %s at %s.",
			IncidentRef(number), EscapeHTML(who), ackedAt.UTC().Format("15:04 UTC"))
	}

	return fmt.Sprintf("✅ %s was already acknowledged at %s.",
		IncidentRef(number), ackedAt.UTC().Format("15:04 UTC"))
}

// BuildIncidentResolvedHTML is the answer to acking a closed incident.
func BuildIncidentResolvedHTML(number int64) string {
	return fmt.Sprintf("🟢 %s is already resolved — nothing to acknowledge.", IncidentRef(number))
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
		body.WriteString(IncidentRef(lines[i].Number))
		body.WriteString("</b> ")
		body.WriteString(EscapeHTML(checkNameOr(lines[i].CheckName)))
	}

	body.WriteString("\n\nSend <code>/ack #42</code> with the one you mean.")

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
