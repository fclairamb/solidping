package watchdog

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Digest is ONE message per run, never one per anomaly. A watchdog that sends
// a message per anomaly is a watchdog that gets muted the first time a region
// strands 400 jobs.
type Digest struct {
	Subject string
	Text    string
}

// Empty reports whether there is nothing worth sending.
func (d Digest) Empty() bool {
	return d.Subject == "" && d.Text == ""
}

// BuildDigest renders the notifiable transitions of one run.
//
// Returns an empty digest when nothing transitioned — a run where every
// anomaly is a known, still-ongoing one inside its quiet window sends
// nothing at all, which is the entire point of the transition model.
func BuildDigest(transitions []Transition, report *Report, generatedAt time.Time) Digest {
	notifiable := NotifiableTransitions(transitions)
	if len(notifiable) == 0 {
		return Digest{}
	}

	active, resolved := splitTransitions(notifiable)

	var body strings.Builder

	body.WriteString(digestHeadline(active, resolved) + "\n")
	body.WriteString("Generated at " + generatedAt.UTC().Format(time.RFC3339) + "\n")

	writeActiveSection(&body, active)
	writeResolvedSection(&body, resolved)
	writeFailureSection(&body, report)

	return Digest{
		Subject: digestSubject(active, resolved),
		Text:    body.String(),
	}
}

// splitTransitions separates the anomalies that need attention from the
// recoveries.
func splitTransitions(transitions []Transition) (active, resolved []Transition) {
	for _, transition := range transitions {
		if transition.Kind == TransitionResolved {
			resolved = append(resolved, transition)

			continue
		}

		active = append(active, transition)
	}

	sort.SliceStable(active, func(i, j int) bool {
		return active[i].Anomaly.Severity > active[j].Anomaly.Severity
	})

	return active, resolved
}

// digestSubject is the one line that has to survive being read on a phone
// lock screen.
func digestSubject(active, resolved []Transition) string {
	if len(active) == 0 {
		return fmt.Sprintf("[SolidPing watchdog] All clear — %d anomaly(ies) recovered", len(resolved))
	}

	critical := 0

	for _, transition := range active {
		if transition.Anomaly.Severity == SeverityCritical {
			critical++
		}
	}

	subject := fmt.Sprintf("[SolidPing watchdog] %d platform anomaly(ies)", len(active))
	if critical > 0 {
		subject += fmt.Sprintf(", %d critical", critical)
	}

	if len(resolved) > 0 {
		subject += fmt.Sprintf(" (+%d recovered)", len(resolved))
	}

	return subject
}

// digestHeadline restates the subject inside the body, so a medium that drops
// subjects (SMS, a Telegram DM) still opens with the summary.
func digestHeadline(active, resolved []Transition) string {
	return digestSubject(active, resolved)
}

// writeActiveSection renders one block per anomaly: what, how bad, since
// when, and the ready-to-run fix.
func writeActiveSection(body *strings.Builder, active []Transition) {
	if len(active) == 0 {
		return
	}

	body.WriteString("\nANOMALIES\n")

	for _, transition := range active {
		anomaly := transition.Anomaly

		body.WriteString(fmt.Sprintf("\n[%s] %s (%s)\n",
			strings.ToUpper(anomaly.Severity.String()),
			transition.Fingerprint,
			transitionLabel(transition)))
		body.WriteString("  " + anomaly.Headline + "\n")

		if anomaly.Detail != "" {
			body.WriteString("  " + anomaly.Detail + "\n")
		}

		if anomaly.Remediation != "" {
			body.WriteString("  fix: " + anomaly.Remediation + "\n")
		}
	}
}

// writeResolvedSection renders the recoveries. They matter as much as the
// anomalies: confirming "the watchdog now sees 0 stranded jobs" is the
// operator's exit criterion after a fix.
func writeResolvedSection(body *strings.Builder, resolved []Transition) {
	if len(resolved) == 0 {
		return
	}

	body.WriteString("\nRECOVERED\n")

	for _, transition := range resolved {
		body.WriteString(fmt.Sprintf("  %s — clear again (first seen %s)\n",
			transition.Fingerprint,
			transition.FirstSeenAt.UTC().Format(time.RFC3339)))
	}
}

// writeFailureSection names the detectors that could not run. A watchdog that
// hides its own blind spots is the bug this spec exists to kill, one level up.
func writeFailureSection(body *strings.Builder, report *Report) {
	if report == nil || !report.HasFailures() {
		return
	}

	names := make([]string, 0, len(report.Failed))
	for name := range report.Failed {
		names = append(names, name)
	}

	sort.Strings(names)

	body.WriteString("\nDETECTOR FAILURES (these anomalies could NOT be evaluated this run)\n")

	for _, name := range names {
		body.WriteString(fmt.Sprintf("  %s: %v\n", name, report.Failed[name]))
	}
}

// transitionLabel renders the transition in the words an operator needs:
// "NEW" or "STILL BROKEN since …".
func transitionLabel(transition Transition) string {
	if transition.Kind == TransitionNew {
		return "NEW"
	}

	return "STILL BROKEN since " + transition.FirstSeenAt.UTC().Format(time.RFC3339)
}
