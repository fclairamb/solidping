package stats

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestProcessingStatsReportConvertsDelayToSeconds is a regression test for
// spec 2026-09-25-07 item 4: report() used to hand ReportedStats.AverageDelay
// (documented, and logged under "averageDelaySeconds", as seconds) the raw
// AverageDelayMs EWMA value with no unit conversion. AverageDelayMs genuinely
// tracks milliseconds — AddMetric feeds it delay.Milliseconds() — so an
// average delay of ~250s (exactly the GetJobWait lateness this spec fixes)
// logged out as "averageDelaySeconds≈250000" was that same figure read
// straight off the millisecond EWMA. report() must now divide by 1000.
//
// White-box (package stats, not stats_test): backdating the EWMA's unexported
// lastTime and ProcessingStats' unexported lastCheck is what makes this
// deterministic without a real wall-clock wait — an EWMA barely moves on an
// update fired immediately after construction (decay ~= 1), and AddMetric
// only calls report() once a full reportingPeriod has elapsed.
func TestProcessingStatsReportConvertsDelayToSeconds(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	ps := NewProcessingStats(time.Minute, time.Minute, slog.Default())

	// dt >> interval makes decay ~= 0, so this Update lands at (approximately)
	// the raw sample instead of a slow blend from zero.
	ps.AverageDelayMs.lastTime = time.Now().Add(-time.Hour)
	ps.AverageDelayMs.Update(250_000) // 250,000ms = 250s — the spec's reported figure

	var reported ReportedStats
	ps.SetReporter(func(s ReportedStats) { reported = s })

	ps.lastCheck = time.Now().Add(-2 * time.Minute) // force AddMetric to report()
	ps.AddMetric(true, 0, 0)

	r.InDeltaf(250.0, reported.AverageDelay, 2.0,
		"AverageDelay must be seconds (~250), not the raw millisecond EWMA value (~250000): got %v",
		reported.AverageDelay)
}
