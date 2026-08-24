package watchdog_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/watchdog"
)

// TestBuildDigestIsOneMessagePerRun: three anomalies produce ONE message. A
// watchdog that sends a message per anomaly is a watchdog that gets muted the
// first time a region strands 400 jobs.
func TestBuildDigestIsOneMessagePerRun(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	transitions := []watchdog.Transition{
		{
			Fingerprint: "dark-region:eu2",
			Kind:        watchdog.TransitionNew,
			FirstSeenAt: pinnedNow,
			Notify:      true,
			Anomaly: watchdog.Anomaly{
				Detector:    watchdog.DetectorDarkRegion,
				Subject:     "eu2",
				Severity:    watchdog.SeverityCritical,
				Headline:    `region "eu2" is DARK (no live worker): 419 job(s) assigned, 345 overdue`,
				Detail:      "liveWorkers=0 checksReferencing=57",
				Remediation: `POST /api/v1/system/regions/migrate {"from":"eu2","to":"<live-region>"}`,
			},
		},
		{
			Fingerprint: "stale-incidents:active",
			Kind:        watchdog.TransitionRenotify,
			FirstSeenAt: pinnedNow.Add(-30 * 60 * 1e9),
			Notify:      true,
			Anomaly: watchdog.Anomaly{
				Detector: watchdog.DetectorStaleIncidents,
				Subject:  watchdog.SubjectStaleIncidents,
				Severity: watchdog.SeverityWarning,
				Headline: "61 active incident(s) are frozen",
			},
		},
		{
			Fingerprint: "fleet-collapse:instance",
			Kind:        watchdog.TransitionResolved,
			FirstSeenAt: pinnedNow.Add(-60 * 60 * 1e9),
			Notify:      true,
		},
	}

	report := reportOf(pinnedNow)
	report.Failed[watchdog.DetectorFleetCollapse] = fmt.Errorf("db timeout: %w", errFixture)

	digest := watchdog.BuildDigest(transitions, report, pinnedNow)

	r.False(digest.Empty())
	r.Contains(digest.Subject, "2 platform anomaly(ies)")
	r.Contains(digest.Subject, "1 critical")
	r.Contains(digest.Subject, "+1 recovered")

	// One message carrying everything.
	r.Equal(1, strings.Count(digest.Text, "ANOMALIES"))
	r.Contains(digest.Text, "dark-region:eu2")
	r.Contains(digest.Text, "(NEW)")
	r.Contains(digest.Text, "STILL BROKEN since")
	r.Contains(digest.Text, "RECOVERED")
	r.Contains(digest.Text, "fleet-collapse:instance")
	r.Contains(digest.Text, "/system/regions/migrate",
		"the ready-to-run remediation is the point of the digest")
	r.Contains(digest.Text, "DETECTOR FAILURES",
		"a blind spot in the watchdog must be visible in the watchdog's own report")

	// Critical sorts above warning so the worst news is not buried.
	r.Less(strings.Index(digest.Text, "dark-region:eu2"), strings.Index(digest.Text, "stale-incidents:active"))
}

// TestBuildDigestSaysNothingWhenNothingTransitioned is the anti-flood contract
// seen from the message side.
func TestBuildDigestSaysNothingWhenNothingTransitioned(t *testing.T) {
	t.Parallel()

	digest := watchdog.BuildDigest([]watchdog.Transition{
		{Fingerprint: "dark-region:eu2", Kind: watchdog.TransitionOngoing, Notify: false},
	}, reportOf(pinnedNow), pinnedNow)

	require.True(t, digest.Empty())
}

// TestBuildDigestAllClear: a run whose only news is a recovery still sends —
// "the watchdog now sees 0 stranded jobs" is the operator's exit criterion.
func TestBuildDigestAllClear(t *testing.T) {
	t.Parallel()

	digest := watchdog.BuildDigest([]watchdog.Transition{
		{
			Fingerprint: "dark-region:eu2",
			Kind:        watchdog.TransitionResolved,
			FirstSeenAt: pinnedNow,
			Notify:      true,
		},
	}, reportOf(pinnedNow), pinnedNow)

	require.Contains(t, digest.Subject, "All clear")
	require.Contains(t, digest.Text, "dark-region:eu2")
}
