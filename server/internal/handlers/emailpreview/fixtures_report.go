package emailpreview

// Preview fixture of the uptime-report template.

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/i18n"
	"github.com/fclairamb/solidping/server/internal/slo"
	"github.com/fclairamb/solidping/server/internal/uptimereport"
)

// fixtureReportZone is the schedule zone of the preview report: its period is
// July in Paris, whatever the previewing reader's zone.
const fixtureReportZone = "Europe/Paris"

// uptimeReportFixture is the fixture for the scheduled uptime-report digest
// (spec 2026-08-20-01). Its figures are written by uptimereport (Data.In) in
// loc, exactly as a recipient's copy is. The keys are PascalCase because that
// is what the template reads and what uptimereport.Data marshals to — see the
// tag comment on that struct for why the two must agree.
//
// It deliberately carries a per-check row, a per-objective row and an
// unsubscribe link, so TestPreview_AllShippedTemplatesRender exercises the
// range blocks and the bulk-mail footer rather than only the header.
func uptimeReportFixture(loc i18n.Locale) map[string]any {
	report := uptimeReportPreview().In(loc)

	checks := make([]map[string]any, 0, len(report.Checks))
	for i := range report.Checks {
		row := &report.Checks[i]

		days := make([]map[string]any, 0, len(row.Days))
		for j := range row.Days {
			day := &row.Days[j]
			days = append(days, map[string]any{keyColor: day.Color, keySpan: day.Span, keyWide: day.Wide})
		}

		checks = append(checks, map[string]any{
			keyName: row.Name, keyHasData: row.HasData, keyAvailability: row.AvailabilityPct,
			"AvailabilityColor": row.AvailabilityColor, "Days": days,
		})
	}

	objective := report.SLOs[0]

	return map[string]any{
		keyOrgName:        report.OrgName,
		"PeriodLabel":     report.PeriodLabel,
		"ScopeLabel":      report.ScopeLabel,
		"Timezone":        report.Timezone,
		keyHasData:        report.HasData,
		keyAvailability:   report.AvailabilityPct,
		"CheckCount":      report.CheckCount,
		"IncidentCount":   report.IncidentCount,
		"LongestIncident": report.LongestIncident,
		"AverageIncident": report.AverageIncident,
		"TotalDowntime":   report.TotalDowntime,

		// Period-over-period trend. Present in the fixture so the preview
		// exercises the delta spans rather than only their absence.
		"PreviousPeriodLabel":     report.PreviousPeriodLabel,
		"HasPreviousData":         report.HasPreviousData,
		"PreviousAvailabilityPct": report.PreviousAvailabilityPct,
		"PreviousIncidentCount":   report.PreviousIncidentCount,
		"PreviousAvgResponseTime": report.PreviousAvgResponseTime,
		"ShowAvailabilityDelta":   report.ShowAvailabilityDelta,
		"AvailabilityDeltaText":   report.AvailabilityDeltaText,
		"AvailabilityDeltaColor":  report.AvailabilityDeltaColor,
		"ShowIncidentDelta":       report.ShowIncidentDelta,
		"IncidentDeltaText":       report.IncidentDeltaText,
		"IncidentDeltaColor":      report.IncidentDeltaColor,
		"ShowResponseDelta":       report.ShowResponseDelta,
		"ResponseDeltaText":       report.ResponseDeltaText,
		"ResponseDeltaColor":      report.ResponseDeltaColor,

		// Response-time block.
		"HasLatency":      report.HasLatency,
		"AvgResponseTime": report.AvgResponseTime,
		"MinResponseTime": report.MinResponseTime,
		"MaxResponseTime": report.MaxResponseTime,
		"SlowLine":        report.SlowLine,
		"SlowNote":        report.SlowNote,
		"LatencyNote":     report.LatencyNote,

		"DayStripLabel": report.DayStripLabel,
		"Checks":        checks,
		"SLOs": []map[string]any{{
			keyName:           objective.Name,
			keyHasData:        objective.HasData,
			"AttainmentPct":   objective.AttainmentPct,
			"TargetPct":       objective.TargetPct,
			"State":           objective.State,
			"StateLabel":      objective.StateLabel,
			"BudgetRemaining": objective.BudgetRemaining,
		}},
		keyDashboardURL:  report.DashboardURL,
		"UnsubscribeURL": "https://solidping.example/unsubscribe?token=preview-unsub-token",
	}
}

// uptimeReportPreview is the measured report behind the fixture: a monthly
// org-wide report for July of this year, in fixtureReportZone.
func uptimeReportPreview() *uptimereport.Data {
	paris, err := i18n.ParseTimezone(fixtureReportZone)
	if err != nil {
		paris = time.UTC
	}

	year := time.Now().Year()
	july := time.Date(year, time.July, 1, 0, 0, 0, 0, paris)
	june := july.AddDate(0, -1, 0)
	attainment := 99.95

	return &uptimereport.Data{
		OrgName:           fixtureOrgName,
		Timezone:          fixtureReportZone,
		HasData:           true,
		AvailabilityColor: colorReportGood,
		CheckCount:        2,
		IncidentCount:     3,

		HasPreviousData:        true,
		PreviousIncidentCount:  5,
		ShowAvailabilityDelta:  true,
		AvailabilityDeltaColor: colorReportGood,
		ShowIncidentDelta:      true,
		IncidentDeltaColor:     colorReportGood,
		ShowResponseDelta:      true,
		ResponseDeltaColor:     "#b91c1c",
		HasLatency:             true,
		StripsShown:            2,

		Checks: []uptimereport.CheckRow{
			{
				Name: fixtureCheckName, HasData: true, Availability: 99.98, AvailabilityColor: colorReportGood,
				Days: []uptimereport.DayCell{
					{Color: colorReportGood, Span: 20, Wide: true},
					{Color: "#b45309", Span: 1},
					{Color: colorReportGood, Span: 10, Wide: true},
				},
			},
			{
				Name: "Marketing site",
				Days: []uptimereport.DayCell{{Color: "#d1d5db", Span: 31, Wide: true}},
			},
		},
		SLOs: []uptimereport.SLORow{{
			Name:       "API availability",
			HasData:    true,
			State:      slo.StateHealthy,
			Attainment: &attainment,
			Target:     99.9,
			Budget:     21*time.Minute + 30*time.Second,
		}},
		DashboardURL: fixtureDashboardURL,

		Figures: uptimereport.Figures{
			Period:               slo.Window{Start: july, End: july.AddDate(0, 1, 0)},
			Previous:             slo.Window{Start: june, End: july},
			OrgWide:              true,
			Availability:         99.95,
			PreviousAvailability: 99.87,
			AvailabilityDelta:    0.08,
			IncidentDelta:        -2,
			ResponseDelta:        8.4,
			LongestIncident:      42 * time.Minute,
			AverageIncident:      21*time.Minute + 40*time.Second,
			TotalDowntime:        time.Hour + 5*time.Minute,
			AvgResponse:          171,
			MinResponse:          42,
			MaxResponse:          3200,
			HasResponseRange:     true,
			PreviousAvgResponse:  158,
			SlowSamples:          4,
			SlowPeaks:            2,
			StripStart:           time.Date(year, time.July, 1, 0, 0, 0, 0, time.UTC),
			StripDays:            31,
		},
	}
}
