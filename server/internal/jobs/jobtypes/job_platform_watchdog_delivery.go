package jobtypes

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
	"github.com/fclairamb/solidping/server/internal/opsnotifywire"
	"github.com/fclairamb/solidping/server/internal/watchdog"
)

// digestTag starts every digest subject, in every language: an operator's
// mail filter matches on it (the region sweep's notices share it).
const digestTag = "[SolidPing watchdog] "

// deliverWatchdogDigest sends ONE digest per run to every configured
// recipient, through the recipient's OWN notification routes.
//
// No new medium and no hardcoded webhook: operators already maintain their
// contact preferences for incident paging, and the watchdog inherits them. The
// only thing this adds is the guarantee that a drop is never silent — every
// recipient that could not be reached is named in the log, because a silent
// drop on an alerting path is the exact bug this spec exists to kill.
//
// The fan-out itself now lives in internal/opsnotify, shared with the
// instance-level operator notifications: it is the same problem ("tell this
// person, on whatever they already set up"), and it used to have the word
// "watchdog" baked into every function name.
//
// The email is written from localized, in the recipient's language; every
// other medium sends the digest's English text.
func deliverWatchdogDigest(
	ctx context.Context, jctx *jobdef.JobContext, log *slog.Logger,
	cfg *watchdog.Config, digest watchdog.Digest, localized *digestCopy,
) {
	if len(cfg.Recipients) == 0 {
		// Enabled with nobody to tell. The anomalies are still logged and
		// metered above; this WARN is what makes the misconfiguration visible
		// instead of letting an operator believe they are covered.
		log.WarnContext(ctx,
			"Platform watchdog is enabled but has no recipients; the digest was logged, not delivered",
			"subject", digest.Subject)

		return
	}

	deps := operatorNoticeDeps(jctx)

	notice := &opsnotify.Notice{
		Event:     opsnotify.EventWatchdogDigest,
		Subject:   digest.Subject,
		Body:      digest.Text,
		Localized: localized,
	}

	// Watchdog recipients are NOT filtered on super_admin. They are a separate,
	// explicitly configured list whose payload is the platform's own vitals —
	// no customer content — unlike operator notifications, which can quote a
	// support thread and therefore resolve against live super-admin status.
	for _, userUID := range cfg.Recipients {
		opsnotify.DeliverToUser(ctx, deps, log, userUID, notice)
	}
}

// operatorNoticeDeps builds the notice transport for a job run.
//
// It goes through the job context rather than a registry field so that a
// minimally wired JobContext (which is what the job unit tests build) still
// gets a working email path.
func operatorNoticeDeps(jctx *jobdef.JobContext) opsnotify.Deps {
	return opsnotifywire.Build(jctx.DBService, jctx.Services, jctx.AppConfig)
}

// digestCopy is the digest of one watchdog run in structured form: the email
// is written from it in the recipient's language and timezone
// (opsnotify.Localizer). It mirrors watchdog.BuildDigest, which stays the
// English text every other medium sends; a test holds the two together.
//
// Each anomaly's headline, detail and remediation, and a failed detector's
// error, are diagnostics written by the detectors: they are quoted as is.
type digestCopy struct {
	transitions []watchdog.Transition
	report      *watchdog.Report
	generatedAt time.Time
}

// Localize implements opsnotify.Localizer.
func (c *digestCopy) Localize(loc i18n.Locale) (string, string) {
	return c.write(loc, func(instant time.Time) string { return loc.Date(instant) })
}

// write renders the digest. date formats an instant: in the reader's zone for
// the email, RFC 3339 UTC in watchdog.BuildDigest's English text.
func (c *digestCopy) write(loc i18n.Locale, date func(time.Time) string) (string, string) {
	active, resolved := splitDigest(watchdog.NotifiableTransitions(c.transitions))
	subject := digestSubject(loc, active, resolved)

	var body strings.Builder

	body.WriteString(subject + "\n")
	body.WriteString(loc.T("notices.watchdogDigest.generatedAt", "date", date(c.generatedAt)) + "\n")

	writeDigestActive(&body, loc, date, active)
	writeDigestResolved(&body, loc, date, resolved)
	writeDigestFailures(&body, loc, c.report)

	return subject, body.String()
}

// splitDigest separates the anomalies from the recoveries, the worst
// anomalies first.
func splitDigest(transitions []watchdog.Transition) ([]watchdog.Transition, []watchdog.Transition) {
	active := make([]watchdog.Transition, 0, len(transitions))
	resolved := make([]watchdog.Transition, 0, len(transitions))

	for i := range transitions {
		if transitions[i].Kind == watchdog.TransitionResolved {
			resolved = append(resolved, transitions[i])

			continue
		}

		active = append(active, transitions[i])
	}

	sort.SliceStable(active, func(i, j int) bool {
		return active[i].Anomaly.Severity > active[j].Anomaly.Severity
	})

	return active, resolved
}

// digestSubject is the one line that has to survive being read on a phone
// lock screen. The body opens with it too, for a reader whose medium shows no
// subject.
func digestSubject(loc i18n.Locale, active, resolved []watchdog.Transition) string {
	if len(active) == 0 {
		return digestTag + loc.Tn("notices.watchdogDigest.allClear", len(resolved))
	}

	critical := 0

	for i := range active {
		if active[i].Anomaly.Severity == watchdog.SeverityCritical {
			critical++
		}
	}

	subject := digestTag + loc.Tn("notices.watchdogDigest.anomalies", len(active))
	if critical > 0 {
		subject += loc.Tn("notices.watchdogDigest.critical", critical)
	}

	if len(resolved) > 0 {
		subject += loc.Tn("notices.watchdogDigest.recovered", len(resolved))
	}

	return subject
}

// writeDigestActive renders one block per anomaly: what, how bad, since when,
// and the ready-to-run fix.
func writeDigestActive(
	body *strings.Builder, loc i18n.Locale, date func(time.Time) string, active []watchdog.Transition,
) {
	if len(active) == 0 {
		return
	}

	body.WriteString("\n" + loc.T("notices.watchdogDigest.anomaliesTitle") + "\n")

	for i := range active {
		anomaly := &active[i].Anomaly

		label := loc.T("notices.watchdogDigest.new")
		if active[i].Kind != watchdog.TransitionNew {
			label = loc.T("notices.watchdogDigest.stillBroken", "date", date(active[i].FirstSeenAt))
		}

		fmt.Fprintf(body, "\n[%s] %s (%s)\n", severityLabel(loc, anomaly.Severity), active[i].Fingerprint, label)
		body.WriteString("  " + anomaly.Headline + "\n")

		if anomaly.Detail != "" {
			body.WriteString("  " + anomaly.Detail + "\n")
		}

		if anomaly.Remediation != "" {
			body.WriteString("  " + loc.T("notices.watchdogDigest.fix", "remediation", anomaly.Remediation) + "\n")
		}
	}
}

// writeDigestResolved renders the recoveries: confirming the watchdog sees the
// problem gone is the operator's exit criterion after a fix.
func writeDigestResolved(
	body *strings.Builder, loc i18n.Locale, date func(time.Time) string, resolved []watchdog.Transition,
) {
	if len(resolved) == 0 {
		return
	}

	body.WriteString("\n" + loc.T("notices.watchdogDigest.recoveredTitle") + "\n")

	for i := range resolved {
		body.WriteString("  " + loc.T("notices.watchdogDigest.clearAgain",
			"fingerprint", resolved[i].Fingerprint, "date", date(resolved[i].FirstSeenAt)) + "\n")
	}
}

// writeDigestFailures names the detectors that could not run.
func writeDigestFailures(body *strings.Builder, loc i18n.Locale, report *watchdog.Report) {
	if report == nil || !report.HasFailures() {
		return
	}

	names := make([]string, 0, len(report.Failed))
	for name := range report.Failed {
		names = append(names, name)
	}

	sort.Strings(names)

	body.WriteString("\n" + loc.T("notices.watchdogDigest.failuresTitle") + "\n")

	for _, name := range names {
		fmt.Fprintf(body, "  %s: %v\n", name, report.Failed[name])
	}
}

// severityLabel is the bracketed severity of an anomaly ("CRITICAL"). An
// unknown severity reads as a warning, as watchdog.Severity.String does.
func severityLabel(loc i18n.Locale, severity watchdog.Severity) string {
	switch severity {
	case watchdog.SeverityInfo:
		return loc.T("notices.watchdogDigest.severity.info")
	case watchdog.SeverityCritical:
		return loc.T("notices.watchdogDigest.severity.critical")
	case watchdog.SeverityWarning:
		return loc.T("notices.watchdogDigest.severity.warning")
	default:
		return loc.T("notices.watchdogDigest.severity.warning")
	}
}
