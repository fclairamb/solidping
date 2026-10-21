package notifications

import (
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/i18n"
)

// reminderEmoji is the identity of `incident.reminder` (spec 2026-10-12-16): an
// alarm clock, so a reminder reads as "still not handled", not as a new outage.
// 🔁 is taken by incident.reopened (one emoji per event type, product-wide).
const reminderEmoji = "⏰"

// fieldLabelStatus labels the "what state is this in" line on card-style
// senders (the unack call to action, the reminder sentence).
const fieldLabelStatus = "Status"

// reminderTitle is the one-line header of a reminder, e.g.
// "⏰ Still down: api-health (#42)". Shared by every sender so a reminder
// reads the same way whichever channel it lands in.
func reminderTitle(payload *Payload) string {
	name := getCheckName(payload.Check)
	ref := strings.TrimSpace(incidentRefPrefix(payload.Incident))

	if ref != "" {
		return reminderEmoji + " Still down: " + name + " (" + ref + ")"
	}

	return reminderEmoji + " Still down: " + name
}

// reminderSentence is the body of a reminder: "Still down after 1 h 15 min,
// not acknowledged (reminder 3)".
func reminderSentence(payload *Payload) string {
	return reminderSentenceIn(i18n.English, payload, time.Now())
}

// reminderSentenceIn is reminderSentence in the reader's language, for a
// reminder written at now.
func reminderSentenceIn(loc i18n.Locale, payload *Payload, now time.Time) string {
	elapsed := reminderDurationIn(loc, reminderElapsed(payload, now))

	if payload.ReminderSeq > 0 {
		return loc.T("incident.reminder.sentenceSeq", "duration", elapsed, "seq", payload.ReminderSeq)
	}

	return loc.T("incident.reminder.sentence", "duration", elapsed)
}

// reminderElapsed is how long the incident has been open at now.
func reminderElapsed(payload *Payload, now time.Time) time.Duration {
	if payload.Incident == nil || payload.Incident.StartedAt.IsZero() {
		return 0
	}

	return now.Sub(payload.Incident.StartedAt)
}

// reminderDurationIn renders an outage length the way a reminder says it:
// "45 min", "1 h", "1 h 15 min", "2 d 3 h" in English. Reminders are at least
// 15 minutes apart, so seconds are noise.
func reminderDurationIn(loc i18n.Locale, span time.Duration) string {
	minutes := int(span.Round(time.Minute) / time.Minute)

	const (
		minutesPerHour = 60
		minutesPerDay  = 24 * minutesPerHour
		key            = "incident.reminder.duration."
	)

	switch {
	case minutes < minutesPerHour:
		return loc.T(key+"minutes", "n", minutes)
	case minutes < minutesPerDay:
		hours, rest := minutes/minutesPerHour, minutes%minutesPerHour
		if rest == 0 {
			return loc.T(key+"hours", "n", hours)
		}

		return loc.T(key+"hoursMinutes", "hours", hours, "minutes", rest)
	default:
		days, hours := minutes/minutesPerDay, (minutes%minutesPerDay)/minutesPerHour
		if hours == 0 {
			return loc.T(key+"days", "n", days)
		}

		return loc.T(key+"daysHours", "days", days, "hours", hours)
	}
}

// reminderPlainBody is the plain-text rendering for channels with no markup:
// the reminder sentence, then the region line when there is one.
func reminderPlainBody(payload *Payload) string {
	body := reminderSentence(payload) + "."

	if line := RegionLine(payload); line != "" {
		body += "\n" + line
	}

	return body
}
