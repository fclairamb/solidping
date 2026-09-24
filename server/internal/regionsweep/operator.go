package regionsweep

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/opsnotify"
	"github.com/fclairamb/solidping/server/internal/regionoutage"
	"github.com/fclairamb/solidping/server/internal/watchdog"
)

// subjectPrefix matches the watchdog digest, so an operator's mail filter
// catches both.
const subjectPrefix = "[SolidPing watchdog] "

// notifyOperatorDark pages the operator about a region that went dark —
// unless the hourly watchdog already did, which the shared anomaly marker
// tells (spec 2026-09-25-03 §2).
func (r *sweepRun) notifyOperatorDark(
	ctx context.Context, state *regionState, marker *regionoutage.Marker, notices []orgNotice,
) bool {
	blind := 0
	for i := range notices {
		blind += notices[i].blind
	}

	jobs, overdue := 0, 0
	if state.row != nil {
		jobs, overdue = state.row.Jobs, state.row.JobsOverdue
	}

	headline := fmt.Sprintf("region %q is DARK (no live worker) since %s: %d job(s) assigned, %d overdue",
		state.slug, marker.Since.UTC().Format(noticeTimeLayout), jobs, overdue)

	body := strings.Join([]string{
		headline + ".",
		fmt.Sprintf("%d check(s) in %d organization(s) run only from this region and are not running; "+
			"each of those organizations was notified by email.", blind, len(notices)),
		fmt.Sprintf("Remediation: bring a worker for %q back, or "+
			"POST /api/v1/system/regions/migrate {\"from\":%q,\"to\":\"<live-region>\"}.", state.slug, state.slug),
	}, "\n\n")

	return r.deliverOperator(ctx, state.slug, watchdog.SeverityCritical, headline,
		subjectPrefix+"Region "+state.slug+" is dark", body)
}

// notifyOperatorStalled pages the operator about live workers that stopped
// claiming. Orgs are not told: the cause is ambiguous and checks may still be
// running late rather than not at all.
func (r *sweepRun) notifyOperatorStalled(
	ctx context.Context, state *regionState, marker *regionoutage.Marker,
) bool {
	live, overdue := 0, 0
	if state.row != nil {
		live, overdue = state.row.LiveWorkers, state.row.JobsOverdue
	}

	headline := fmt.Sprintf("region %q is stalled: %d live worker(s) but %d job(s) overdue, oldest since %s",
		state.slug, live, overdue, marker.Since.UTC().Format(noticeTimeLayout))

	body := strings.Join([]string{
		headline + ".",
		"The workers beat but do not claim. Organizations were not notified.",
		"Inspect: GET /api/v1/system/regions/health, then the workers' logs.",
	}, "\n\n")

	return r.deliverOperator(ctx, state.slug, watchdog.SeverityWarning, headline,
		subjectPrefix+"Region "+state.slug+" is stalled", body)
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

	duration := r.now.Sub(marker.Since)
	body := fmt.Sprintf("Region %q is healthy again after %s (%s since %s). %d organization(s) were told.",
		state.slug, humanDuration(duration), marker.Phase, marker.Since.UTC().Format(time.RFC3339),
		len(marker.NotifiedOrgs))

	r.send(ctx, &opsnotify.Notice{
		Event:   opsnotify.EventWatchdogRegion,
		Subject: subjectPrefix + "Region " + state.slug + " recovered after " + humanDuration(duration),
		Body:    body,
	})

	return true
}

// deliverOperator sends one transition notice if there is someone to send it
// to, the severity passes the watchdog's bar, and the shared marker says
// nobody told the operator already.
func (r *sweepRun) deliverOperator(
	ctx context.Context, region string, severity watchdog.Severity, headline, subject, body string,
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
		watchdog.DarkRegionFingerprint(region), severity, headline, r.now)
	if err != nil {
		r.deps.Logger.ErrorContext(ctx, "Region sweep could not read the shared anomaly marker; notifying anyway",
			"region", region, "error", err)

		claimed = true
	}

	if !claimed {
		r.deps.Logger.InfoContext(ctx, "Region transition already reported by the platform watchdog",
			"region", region)

		return false
	}

	r.send(ctx, &opsnotify.Notice{Event: opsnotify.EventWatchdogRegion, Subject: subject, Body: body})

	return true
}

// send fans one notice out to every recipient on their own routes.
func (r *sweepRun) send(ctx context.Context, notice *opsnotify.Notice) {
	for _, userUID := range r.operators.recipients {
		opsnotify.DeliverToUser(ctx, r.deps.Operator, r.deps.Logger, userUID, notice)
	}
}
