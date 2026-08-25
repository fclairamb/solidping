package notifications

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// burnIncident builds a burn incident the way the evaluator writes one, with
// the details map already round-tripped through JSON types (float64 numbers, an
// RFC3339 timestamp string) — which is how it comes back out of the database
// and therefore how every sender will actually see it.
func burnIncident(t *testing.T) *models.Incident {
	t.Helper()

	incident := models.NewIncident("org", "check", time.Now(), "Fast burn: acme api at 31.0x")
	incident.Kind = models.IncidentKindSLOBurn
	incident.Details = models.JSONMap{
		"slo_name":                 "acme api",
		"slo_alert_policy_kind":    models.SLOAlertPolicyKindFast,
		"severity":                 models.SLOAlertSeverityCritical,
		"burn_threshold":           14.4,
		"burn_rate_long":           31.0,
		"burn_rate_short":          44.5,
		"burn_peak":                52.0,
		"long_window_seconds":      3600.0,
		"short_window_seconds":     300.0,
		"budget_remaining_seconds": 5400.0,
		"projected_exhaustion_at":  "2026-08-23T04:30:00Z",
		"target_pct":               99.9,
	}

	return incident
}

// TestBurnInfoForIgnoresCheckIncidents: everything downstream branches on this,
// so a false positive would rewrite a real outage's message into a budget
// report.
func TestBurnInfoForIgnoresCheckIncidents(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Nil(BurnInfoFor(nil))

	plain := models.NewIncident("org", "check", time.Now(), "api is down")
	r.Nil(BurnInfoFor(plain))
}

// TestBurnInfoReadsJSONRoundTrippedDetails is the real risk here: the map is
// written with native Go types and read back with float64s and strings.
func TestBurnInfoReadsJSONRoundTrippedDetails(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	burn := BurnInfoFor(burnIncident(t))

	r.NotNil(burn)
	r.Equal("acme api", burn.SLOName)
	r.Equal("Fast burn", burn.PolicyLabel)
	r.InDelta(31.0, burn.LongRate, 0.001)
	r.InDelta(44.5, burn.ShortRate, 0.001)
	r.InDelta(52.0, burn.PeakRate, 0.001)
	r.Equal(time.Hour, burn.LongWindow)
	r.Equal(5*time.Minute, burn.ShortWindow)
	r.Equal(90*time.Minute, burn.BudgetRemaining)
	r.Equal(2026, burn.ProjectedExhaustion.Year())
}

// TestBurnSummaryCarriesTheThreeDecidingNumbers: a page that omits any of them
// forces the reader back to the dashboard, which is what paging exists to
// avoid.
func TestBurnSummaryCarriesTheThreeDecidingNumbers(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	burn := BurnInfoFor(burnIncident(t))
	lines := burn.SummaryLines()

	r.Len(lines, 3)
	r.Contains(lines[0], "31.0x")
	r.Contains(lines[0], "14.4x")
	r.Contains(lines[1], "1h30m")
	r.Contains(lines[2], "2026-08-23 04:30:00 UTC")
}

// TestExhaustedBudgetNeverRendersANegativeDuration: "-3h12m remaining" is not a
// sentence anyone should have to parse at 3am.
func TestExhaustedBudgetNeverRendersANegativeDuration(t *testing.T) {
	t.Parallel()

	incident := burnIncident(t)
	incident.Details["budget_remaining_seconds"] = -4200.0

	require.Equal(t, "exhausted", BurnInfoFor(incident).BudgetRemainingText())
}

// TestUnprojectedExhaustionSaysSo rather than rendering a zero timestamp.
func TestUnprojectedExhaustionSaysSo(t *testing.T) {
	t.Parallel()

	incident := burnIncident(t)
	delete(incident.Details, "projected_exhaustion_at")

	require.Equal(t, "not within this window", BurnInfoFor(incident).ProjectedExhaustionText())
}

// TestBurnTemplateSelectionFollowsTheIncidentKind: burn alerts ride the same
// incident.created / incident.resolved events a check outage does, so the event
// type alone cannot pick the template — "[DOWN] api is down" would be an
// outright lie about an objective that merely spent budget too fast.
func TestBurnTemplateSelectionFollowsTheIncidentKind(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	burn := burnIncident(t)
	plain := models.NewIncident("org", "check", time.Now(), "api is down")

	name, ok := burnTemplateForEvent(burn, eventTypeIncidentCreated)
	r.True(ok)
	r.Equal("incident-burn-created.html", name)

	name, ok = burnTemplateForEvent(burn, eventTypeIncidentResolved)
	r.True(ok)
	r.Equal("incident-burn-resolved.html", name)

	_, ok = burnTemplateForEvent(plain, eventTypeIncidentCreated)
	r.False(ok, "a check incident must keep the ordinary outage template")

	_, ok = burnTemplateForEvent(burn, eventTypeIncidentComment)
	r.False(ok, "a comment is about the paging cycle, not the burn numbers")
}

// TestBurnViewModelExposesTheTemplateKeys guards the templates against a silent
// rename: html/template renders a missing key as "<no value>" rather than
// failing, so only an explicit assertion catches it.
func TestBurnViewModelExposesTheTemplateKeys(t *testing.T) {
	t.Parallel()

	viewModel := map[string]any{}
	applyBurnViewModel(viewModel, BurnInfoFor(burnIncident(t)))

	for _, key := range []string{
		"SLOName", "BurnPolicyLabel", "BurnSeverity", "BurnRate", "BurnShortRate",
		"BurnPeakRate", "BurnThreshold", "BurnLongWindow", "BurnShortWindow",
		"BurnBudgetRemaining", "BurnProjectedExhaustion", "BurnTarget",
	} {
		require.Contains(t, viewModel, key)
	}

	require.Equal(t, "31.0x", viewModel["BurnRate"])
	require.Equal(t, "1h30m", viewModel["BurnBudgetRemaining"])
	require.Equal(t, "99.9%", viewModel["BurnTarget"])
}

func TestHumanDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   time.Duration
		want string
	}{
		{in: 0, want: "0m"},
		{in: -time.Hour, want: "0m"},
		{in: 5 * time.Minute, want: "5m"},
		{in: 90 * time.Minute, want: "1h30m"},
		{in: 6 * time.Hour, want: "6h"},
		{in: 50 * time.Hour, want: "2d2h"},
		{in: 48 * time.Hour, want: "2d"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, humanDuration(tt.in))
		})
	}
}
