package emailpreview

// Preview fixture of the uptime-report template.

import (
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// uptimeReportFixture is the fixture for the scheduled uptime-report digest
// (spec 2026-08-20-01). The keys are PascalCase because that is what the
// template reads and what uptimereport.Data marshals to — see the tag comment
// on that struct for why the two must agree.
//
// It deliberately carries a per-check row, a per-objective row and an
// unsubscribe link, so TestPreview_AllShippedTemplatesRender exercises the
// range blocks and the bulk-mail footer rather than only the header.
func uptimeReportFixture(_ i18n.Locale) map[string]any {
	return map[string]any{
		keyOrgName:        fixtureOrgName,
		"PeriodLabel":     "July 2026",
		"ScopeLabel":      "All checks (2)",
		"Timezone":        "Europe/Paris",
		keyHasData:        true,
		keyAvailability:   "99.950",
		"CheckCount":      2,
		"IncidentCount":   3,
		"LongestIncident": "42m",
		"AverageIncident": "21m 40s",
		"TotalDowntime":   "1h 5m",

		// Period-over-period trend. Present in the fixture so the preview
		// exercises the delta spans rather than only their absence.
		"PreviousPeriodLabel":     "June 2026",
		"HasPreviousData":         true,
		"PreviousAvailabilityPct": "99.870",
		"PreviousIncidentCount":   5,
		"PreviousAvgResponseTime": "158 ms",
		"ShowAvailabilityDelta":   true,
		"AvailabilityDeltaText":   "+0.080 pts",
		"AvailabilityDeltaColor":  colorReportGood,
		"ShowIncidentDelta":       true,
		"IncidentDeltaText":       "-2",
		"IncidentDeltaColor":      colorReportGood,
		"ShowResponseDelta":       true,
		"ResponseDeltaText":       "+8.4%",
		"ResponseDeltaColor":      "#b91c1c",

		// Response-time block.
		"HasLatency":      true,
		"AvgResponseTime": "171 ms",
		"MinResponseTime": "42 ms",
		"MaxResponseTime": "3.20 s",
		"SlowLine":        "4 samples and 2 peaks above 1 s",
		"SlowNote":        "A peak is a rolled-up period whose slowest sample exceeded 1 s.",
		"LatencyNote":     "Response times include failed samples.",

		"DayStripLabel": "Daily availability, 1 Jul – 31 Jul (UTC)",
		"Checks": []map[string]any{
			{
				keyName: fixtureCheckName, keyHasData: true, keyAvailability: "99.980",
				"Days": []map[string]any{
					{keyColor: colorReportGood, keySpan: 20, keyWide: true},
					{keyColor: "#b45309", keySpan: 1},
					{keyColor: colorReportGood, keySpan: 10, keyWide: true},
				},
			},
			{
				keyName: "Marketing site", keyHasData: false, keyAvailability: "",
				"Days": []map[string]any{{keyColor: "#d1d5db", keySpan: 31, keyWide: true}},
			},
		},
		"SLOs": []map[string]any{
			{
				keyName:           "API availability",
				keyHasData:        true,
				"AttainmentPct":   "99.950",
				"TargetPct":       "99.900",
				"StateLabel":      "Healthy",
				"BudgetRemaining": "21m 30s",
			},
		},
		keyDashboardURL:  fixtureDashboardURL,
		"UnsubscribeURL": "https://solidping.example/unsubscribe?token=preview-unsub-token",
	}
}
