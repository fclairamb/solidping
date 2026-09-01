package uptimereport

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/email"
)

// gmailClipBytes is the size at which Gmail truncates a message body and hides
// the rest behind a "[Message clipped]" link — which in this mail would swallow
// the objectives, the dashboard button and the unsubscribe footer.
const gmailClipBytes = 102 * 1024

// sizeBudgetBytes is the bar this report is held to: comfortably under the clip
// threshold, not flush against it. The mail still has to survive a MIME
// envelope and quoted-printable encoding on the way out.
const sizeBudgetBytes = 88 * 1024

// worstCaseMonthlyReport is the pathological fixture the strip budget exists
// for: maxCheckRows checks over a 31-day month in which every single day
// differs in color from its neighbor, so the run-length encoding buys nothing
// and every day is its own cell.
func worstCaseMonthlyReport(t *testing.T) *Data {
	t.Helper()

	const days = 31

	palette := []string{
		dayGoodColor, dayWarnColor, dayPoorColor, dayBadColor, dayNoDataColor,
	}

	data := &Data{
		OrgName:                 "acme",
		BrandName:               "acme",
		PeriodLabel:             "July 2026",
		PreviousPeriodLabel:     "June 2026",
		ScopeLabel:              fmt.Sprintf("All checks (%d)", maxCheckRows+87),
		Timezone:                "Europe/Paris",
		HasData:                 true,
		AvailabilityPct:         "97.512",
		AvailabilityColor:       "#c2410c",
		CheckCount:              maxCheckRows + 87,
		IncidentCount:           42,
		LongestIncident:         "3h 12m",
		AverageIncident:         "18m",
		TotalDowntime:           "12h 36m",
		HasPreviousData:         true,
		PreviousAvailabilityPct: "99.870",
		ShowAvailabilityDelta:   true,
		AvailabilityDeltaText:   "-2.358 pts",
		AvailabilityDeltaColor:  deltaBadColor,
		ShowIncidentDelta:       true,
		IncidentDeltaText:       "+37",
		IncidentDeltaColor:      deltaBadColor,
		ShowResponseDelta:       true,
		ResponseDeltaText:       "+41.2%",
		ResponseDeltaColor:      deltaBadColor,
		HasLatency:              true,
		AvgResponseTime:         "412 ms",
		MinResponseTime:         "37 ms",
		MaxResponseTime:         "9.80 s",
		SlowLine:                slowLine(318, 24),
		SlowNote:                "A peak is a rolled-up period whose slowest sample exceeded 1 s.",
		LatencyNote:             "Response times include failed samples.",
		DayStripLabel:           "Daily availability, 1 Jul – 31 Jul (UTC)",
		Truncated:               true,
		TruncatedShown:          maxCheckRows,
		TruncatedTotal:          maxCheckRows + 87,
		DashboardURL:            "https://solidping.example/dash0",
		UnsubscribeURL:          "https://solidping.example/unsubscribe?token=abcdef0123456789",
		Checks:                  make([]CheckRow, 0, maxCheckRows),
	}

	for i := range maxCheckRows {
		colors := make([]string, 0, days)
		for day := range days {
			colors = append(colors, palette[(i+day)%len(palette)])
		}

		cells := encodeDayCells(colors)
		require.Len(t, cells, days, "the fixture must defeat the run-length encoding")

		data.Checks = append(data.Checks, CheckRow{
			Name:              fmt.Sprintf("check-with-a-fairly-long-name-%02d.acme.com", i),
			HasData:           true,
			AvailabilityPct:   "97.512",
			AvailabilityColor: "#c2410c",
			URL:               fmt.Sprintf("https://solidping.example/dash0/orgs/acme/checks/chk-%02d", i),
			Days:              cells,
		})
	}

	// Exactly what Build does before handing the view model to the mailer.
	applyStripBudget(data.Checks)

	return data
}

// TestUptimeReportStaysUnderGmailClipLimit is the size guard the spec asks for:
// a 50-check monthly report with per-day strips must stay well under Gmail's
// clipping threshold, in its WORST case rather than its typical one.
func TestUptimeReportStaysUnderGmailClipLimit(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	data := worstCaseMonthlyReport(t)

	formatter, err := email.NewFormatter()
	r.NoError(err)

	raw, err := json.Marshal(data)
	r.NoError(err)

	var asMap any

	r.NoError(json.Unmarshal(raw, &asMap))

	_, html, _, err := formatter.Format(TemplateName, asMap)
	r.NoError(err)

	// The assertion below is only meaningful if the fixture really rendered:
	// 50 rows, the last check name present, and a real pile of day cells.
	r.Contains(html, "check-with-a-fairly-long-name-49.acme.com")
	r.Equal(maxCheckRows, strings.Count(html, "acme.com</a>"))

	cells := strings.Count(html, "bgcolor=")
	r.Positive(cells)
	r.LessOrEqual(cells, maxStripCells)

	r.Lessf(len(html), sizeBudgetBytes,
		"rendered report is %d bytes; Gmail clips around %d", len(html), gmailClipBytes)
	r.Less(len(html), gmailClipBytes)
}

// TestStripBudgetDropsWholeStripsFromTheBottom pins the degradation: the budget
// is spent worst-first, a row either keeps its WHOLE strip or none of it, and
// the top rows — the ones the digest exists to surface — always keep theirs.
func TestStripBudgetDropsWholeStripsFromTheBottom(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const days = 31

	rows := make([]CheckRow, 0, maxCheckRows)
	for range maxCheckRows {
		cells := make([]DayCell, 0, days)
		for day := range days {
			cells = append(cells, DayCell{Color: dayGoodColor, Span: 1})
			_ = day
		}

		rows = append(rows, CheckRow{Name: "check", HasData: true, Days: cells})
	}

	applyStripBudget(rows)

	total, withStrip := 0, 0

	for _, row := range rows {
		if len(row.Days) == 0 {
			continue
		}

		r.Len(row.Days, days, "a strip is never rendered half-drawn")

		withStrip++
		total += len(row.Days)
	}

	r.LessOrEqual(total, maxStripCells)
	r.Positive(withStrip, "the budget must not wipe out every strip")
	r.Less(withStrip, maxCheckRows, "this fixture is meant to exceed the budget")
	r.NotEmpty(rows[0].Days, "the worst check keeps its strip")
	r.Empty(rows[maxCheckRows-1].Days, "the healthy tail is what loses it")

	// A report small enough to fit keeps every strip — the positive control.
	small := rows[:10]
	for i := range small {
		cells := make([]DayCell, days)
		for day := range cells {
			cells[day] = DayCell{Color: dayGoodColor, Span: 1}
		}

		small[i].Days = cells
	}

	applyStripBudget(small)

	for i := range small {
		r.Len(small[i].Days, days)
	}
}
