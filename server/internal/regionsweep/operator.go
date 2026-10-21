package regionsweep

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/opsnotify"
	"github.com/fclairamb/solidping/server/internal/regionoutage"
	"github.com/fclairamb/solidping/server/internal/watchdog"
)

// subjectPrefix matches the watchdog digest, so an operator's mail filter
// catches both. It reads the same in every language.
const subjectPrefix = "[SolidPing watchdog] "

// Region transition notice kinds.
const (
	noticeDark      = "dark"
	noticeStalled   = "stalled"
	noticeRecovered = "recovered"
)

// regionCopy is one region transition notice in structured form. Every
// medium sends its English text (english), except email, which is written
// in the operator's language and timezone (Localize, opsnotify.Localizer).
type regionCopy struct {
	kind   string
	region string
	// since is when the outage started.
	since time.Time
	// jobs, overdue and live are the region's assigned jobs, overdue jobs
	// and live workers.
	jobs, overdue, live int
	// blind checks in orgs organizations run only from the dark region;
	// replaced automatically placed checks were moved elsewhere.
	blind, orgs, replaced int
	// phase, duration and told describe a recovered outage: the phase it was
	// in, how long it lasted, how many organizations were told.
	phase    regionoutage.Phase
	duration time.Duration
	told     int
}

// Localize implements opsnotify.Localizer.
func (c *regionCopy) Localize(loc i18n.Locale) (string, string) {
	return c.write(loc, false)
}

// notice is the notice every medium gets, English text included.
func (c *regionCopy) notice() *opsnotify.Notice {
	subject, body := c.write(i18n.English, true)

	return &opsnotify.Notice{Event: opsnotify.EventWatchdogRegion, Subject: subject, Body: body, Localized: c}
}

// sinceText formats the outage start: today's UTC layouts in the English text
// (legacy), the reader's zone in the email.
func (c *regionCopy) sinceText(loc i18n.Locale, legacy bool) string {
	switch {
	case !legacy:
		return loc.Date(c.since)
	case c.kind == noticeRecovered:
		return c.since.UTC().Format(time.RFC3339)
	default:
		return c.since.UTC().Format(noticeTimeLayout)
	}
}

// headline is the one-line summary of a dark or stalled region. Its English
// form is also what the shared anomaly marker records.
func (c *regionCopy) headline(loc i18n.Locale, legacy bool) string {
	since := c.sinceText(loc, legacy)

	if c.kind == noticeStalled {
		return loc.Tn("notices.watchdogRegion.stalledHeadline", c.overdue, "region", c.region,
			"workers", loc.Tn("notices.watchdogRegion.liveWorkers", c.live), "since", since)
	}

	return loc.Tn("notices.watchdogRegion.darkHeadline", c.jobs, "region", c.region,
		"since", since, "overdue", c.overdue)
}

// write renders the subject and body. legacy keeps today's English date and
// duration formats, for the media that are not localized.
func (c *regionCopy) write(loc i18n.Locale, legacy bool) (string, string) {
	switch c.kind {
	case noticeStalled:
		return subjectPrefix + loc.T("notices.watchdogRegion.subjectStalled", "region", c.region),
			strings.Join([]string{
				c.headline(loc, legacy) + ".",
				loc.T("notices.watchdogRegion.stalledCause"),
				loc.T("notices.watchdogRegion.stalledInspect"),
			}, "\n\n")
	case noticeRecovered:
		duration := loc.Duration(c.duration)
		if legacy {
			duration = humanDuration(c.duration)
		}

		return subjectPrefix + loc.T("notices.watchdogRegion.subjectRecovered",
				"region", c.region, "duration", duration),
			loc.Tn("notices.watchdogRegion.recovered", c.told, "region", c.region, "duration", duration,
				"phase", phaseLabel(loc, c.phase), "since", c.sinceText(loc, legacy))
	default:
		return subjectPrefix + loc.T("notices.watchdogRegion.subjectDark", "region", c.region),
			strings.Join([]string{
				c.headline(loc, legacy) + ".",
				loc.Tn("notices.watchdogRegion.darkChecks", c.blind,
					"orgs", loc.Tn("notices.watchdogRegion.organizations", c.orgs)),
				loc.Tn("notices.watchdogRegion.moved", c.replaced),
				loc.T("notices.watchdogRegion.darkRemediation", "region", c.region, "command",
					fmt.Sprintf(`POST /api/v1/system/regions/migrate {"from":%q,"to":"<live-region>"}`, c.region)),
			}, "\n\n")
	}
}

// phaseLabel names the phase a recovered outage was in.
func phaseLabel(loc i18n.Locale, phase regionoutage.Phase) string {
	switch phase {
	case regionoutage.PhaseDark:
		return loc.T("notices.watchdogRegion.phase.dark")
	case regionoutage.PhaseStalled:
		return loc.T("notices.watchdogRegion.phase.stalled")
	default:
		return string(phase)
	}
}

// notifyOperatorDark pages the operator about a region that went dark —
// unless the hourly watchdog already did, which the shared anomaly marker
// tells (spec 2026-09-25-03 §2).
func (r *sweepRun) notifyOperatorDark(
	ctx context.Context, state *regionState, marker *regionoutage.Marker, notices []orgNotice, replaced int,
) bool {
	notice := &regionCopy{
		kind: noticeDark, region: state.slug, since: marker.Since,
		orgs: len(notices), replaced: replaced,
	}

	for i := range notices {
		notice.blind += notices[i].blind
	}

	if state.row != nil {
		notice.jobs, notice.overdue = state.row.Jobs, state.row.JobsOverdue
	}

	// Escalating from our own stalled phase is news even if the operator
	// heard about the stall; anything else already on the ledger came from
	// the digest and covers this outage.
	escalation := state.marker != nil && state.marker.Phase == regionoutage.PhaseStalled

	return r.deliverOperator(ctx, watchdog.SeverityCritical, escalation, notice)
}

// notifyOperatorStalled pages the operator about live workers that stopped
// claiming. Orgs are not told: the cause is ambiguous and checks may still be
// running late rather than not at all.
func (r *sweepRun) notifyOperatorStalled(
	ctx context.Context, state *regionState, marker *regionoutage.Marker,
) bool {
	notice := &regionCopy{kind: noticeStalled, region: state.slug, since: marker.Since}
	if state.row != nil {
		notice.live, notice.overdue = state.row.LiveWorkers, state.row.JobsOverdue
	}

	return r.deliverOperator(ctx, watchdog.SeverityWarning, false, notice)
}

// notifyOperatorRecovered closes the operator side of an outage. The shared
// marker is cleared either way; the operator only hears about the recovery
// if someone (this sweep or the digest) told them about the outage.
func (r *sweepRun) notifyOperatorRecovered(
	ctx context.Context, state *regionState, marker *regionoutage.Marker,
) bool {
	existed, err := watchdog.ClearNotification(ctx, r.deps.DB, watchdog.DarkRegionFingerprint(state.slug))
	if err != nil {
		r.deps.Logger.ErrorContext(ctx, "Region sweep could not clear the shared anomaly marker",
			"region", state.slug, "error", err)
	}

	if !existed || len(r.operators.recipients) == 0 {
		return false
	}

	r.send(ctx, (&regionCopy{
		kind: noticeRecovered, region: state.slug, since: marker.Since,
		phase: marker.Phase, duration: r.now.Sub(marker.Since), told: len(marker.NotifiedOrgs),
	}).notice())

	return true
}

// deliverOperator sends one transition notice if there is someone to send it
// to, the severity passes the watchdog's bar, and the shared marker says
// nobody told the operator already.
func (r *sweepRun) deliverOperator(
	ctx context.Context, severity watchdog.Severity, escalation bool, notice *regionCopy,
) bool {
	if len(r.operators.recipients) == 0 {
		// Nobody to tell (watchdog disabled or no recipients). The transition
		// is still logged by the caller and metered. The shared marker is NOT
		// written: a ledger entry for a notice that never went out would
		// suppress the digest's if the watchdog is enabled mid-outage.
		return false
	}

	if severity < r.operators.minSeverity {
		return false
	}

	claimed, err := watchdog.ClaimNotification(ctx, r.deps.DB,
		watchdog.DarkRegionFingerprint(notice.region), severity, notice.headline(i18n.English, true),
		r.now, escalation)
	if err != nil {
		r.deps.Logger.ErrorContext(ctx, "Region sweep could not read the shared anomaly marker; notifying anyway",
			"region", notice.region, "error", err)

		claimed = true
	}

	if !claimed {
		r.deps.Logger.InfoContext(ctx, "Region transition already reported by the platform watchdog",
			"region", notice.region)

		return false
	}

	r.send(ctx, notice.notice())

	return true
}

// send fans one notice out to every recipient on their own routes.
func (r *sweepRun) send(ctx context.Context, notice *opsnotify.Notice) {
	for _, userUID := range r.operators.recipients {
		opsnotify.DeliverToUser(ctx, r.deps.Operator, r.deps.Logger, userUID, notice)
	}
}
