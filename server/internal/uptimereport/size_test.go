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
		PreviousIncidentCount:   5,
		PreviousAvgResponseTime: "292 ms",
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

	// Exactly what Build does before handing the view model to the mailer,
	// including the note it raises when the budget drops a strip — the test
	// must never hand-write that text, or it would pass while the builder
	// stayed silent.
	shown, wanted := applyStripBudget(data.Checks)

	data.StripsShown = shown
	if wanted > shown {
		data.ShowStripBudgetNote = true
		data.StripBudgetNote = stripBudgetNote(shown)
	}

	return data
}

// fittingMonthlyReport is the same shape as worstCaseMonthlyReport but with
// strips the budget comfortably absorbs — the positive control for every
// "the note fired" assertion below.
func fittingMonthlyReport(t *testing.T) *Data {
	t.Helper()

	data := worstCaseMonthlyReport(t)

	// One run-length-encoded cell per row: 50 cells against a 900 budget.
	for i := range data.Checks {
		data.Checks[i].Days = []DayCell{{Color: dayGoodColor, Span: 31, Wide: true}}
	}

	data.StripsShown = 0
	data.ShowStripBudgetNote = false
	data.StripBudgetNote = ""

	shown, wanted := applyStripBudget(data.Checks)

	data.StripsShown = shown
	if wanted > shown {
		data.ShowStripBudgetNote = true
		data.StripBudgetNote = stripBudgetNote(shown)
	}

	return data
}

// renderReport runs the view model through the production path: marshaled to
// the email job's config and read back as a map, then formatted.
func renderReport(t *testing.T, data *Data) (string, string) {
	t.Helper()

	formatter, err := email.NewFormatter()
	require.NoError(t, err)

	raw, err := json.Marshal(data)
	require.NoError(t, err)

	var asMap any

	require.NoError(t, json.Unmarshal(raw, &asMap))

	_, html, text, err := formatter.Format(TemplateName, asMap)
	require.NoError(t, err)

	return html, text
}

// TestUptimeReportStaysUnderGmailClipLimit is the size guard the spec asks for:
// a 50-check monthly report with per-day strips must stay well under Gmail's
// clipping threshold, in its WORST case rather than its typical one.
func TestUptimeReportStaysUnderGmailClipLimit(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	data := worstCaseMonthlyReport(t)
	html, _ := renderReport(t, data)

	// The assertion below is only meaningful if the fixture really rendered:
	// 50 rows, the last check name present, and a real pile of day cells.
	r.Contains(html, "check-with-a-fairly-long-name-49.acme.com")
	r.Equal(maxCheckRows, strings.Count(html, "acme.com</a>"))

	cells := strings.Count(html, "bgcolor=")
	r.Positive(cells)
	r.LessOrEqual(cells, maxStripCells)

	t.Logf("worst-case 50-check monthly report renders to %d bytes (%d day cells)", len(html), cells)

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

	shown, wanted := applyStripBudget(rows)

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

	// The counts the template's note is built from must describe what actually
	// happened, or the digest would state a number the reader can disprove by
	// counting the bars.
	r.Equal(withStrip, shown)
	r.Equal(maxCheckRows, wanted)
	r.Less(shown, wanted, "this fixture must report a real degradation")
	r.Contains(stripBudgetNote(shown), fmt.Sprintf("%d lowest-uptime checks", shown))

	// A report small enough to fit keeps every strip — the positive control.
	small := rows[:10]
	for i := range small {
		cells := make([]DayCell, days)
		for day := range cells {
			cells[day] = DayCell{Color: dayGoodColor, Span: 1}
		}

		small[i].Days = cells
	}

	smallShown, smallWanted := applyStripBudget(small)

	for i := range small {
		r.Len(small[i].Days, days)
	}

	// Nothing was dropped, so the builder raises no note at all.
	r.Equal(len(small), smallShown)
	r.Equal(smallWanted, smallShown)
}

// TestUptimeReportSaysWhenStripsWereDropped is the audit gap this closes: when
// the payload budget drops a row's strip, the row still shows a percentage but
// no colored bar — which is indistinguishable from a broken email unless the
// digest says what happened. The row cap already had to say "showing the 50
// lowest-uptime checks of N"; the same rule applies here.
func TestUptimeReportSaysWhenStripsWereDropped(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	data := worstCaseMonthlyReport(t)

	// The fixture really did lose strips, so the assertions below are about a
	// real degradation rather than a hypothetical one.
	r.True(data.ShowStripBudgetNote)
	r.Positive(data.StripsShown)
	r.Less(data.StripsShown, len(data.Checks))

	withStrip := 0
	for _, row := range data.Checks {
		if len(row.Days) > 0 {
			withStrip++
		}
	}

	r.Equal(withStrip, data.StripsShown, "the note must count the rows that actually kept a strip")

	html, text := renderReport(t, data)

	for _, body := range []string{html, text} {
		r.NotContains(body, "<no value>")
		r.Containsf(body, fmt.Sprintf("shown for the %d lowest-uptime checks", data.StripsShown),
			"the note must name how many rows kept their strip")
		// Factual, not a state signal: no color semantics on this line.
		r.Contains(body, "keep this email under the size mail clients truncate at")
	}
}

// TestUptimeReportOmitsTheStripNoteWhenEverythingFits is the negative half: a
// report whose strips all fit must not carry the note at all. Same template,
// same fixture shape as the test above — only the strip payload differs.
func TestUptimeReportOmitsTheStripNoteWhenEverythingFits(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	data := fittingMonthlyReport(t)

	r.False(data.ShowStripBudgetNote)
	r.Empty(data.StripBudgetNote)
	r.Equal(len(data.Checks), data.StripsShown, "every row kept its strip")

	html, text := renderReport(t, data)

	for _, body := range []string{html, text} {
		r.NotContains(body, "<no value>")
		r.NotContains(body, "lowest-uptime checks;")
		r.NotContains(body, "keep this email under the size mail clients truncate at")
		// The strip itself, and the legend, are still there — the note is the
		// only thing missing.
		r.Contains(body, "worst first")
	}

	r.Contains(html, `bgcolor="#15803d"`)
	r.Equal(len(data.Checks), strings.Count(html, "bgcolor="))
}

// TestUptimeReportSaysWorstFirstEvenWithoutAStrip: the table is sorted
// worst-first whether or not the strip query succeeded, so the wording must not
// disappear with the strip.
func TestUptimeReportSaysWorstFirstEvenWithoutAStrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	data := worstCaseMonthlyReport(t)

	// What Build produces when dayStrips fails and logs: rows, no strips.
	data.DayStripLabel = ""
	data.StripsShown = 0
	data.ShowStripBudgetNote = false
	data.StripBudgetNote = ""

	for i := range data.Checks {
		data.Checks[i].Days = nil
	}

	html, text := renderReport(t, data)

	for _, body := range []string{html, text} {
		r.NotContains(body, "<no value>")
		r.Contains(body, "worst first")
		// No strip legend promising a strip that is not there.
		r.NotContains(body, "green ≥ 99.9%")
		r.NotContains(body, "Gray means nothing was recorded")
	}

	r.NotContains(html, "bgcolor=\"#15803d\"")
}

// TestUptimeReportRendersAZeroPreviousIncidentCount is a regression guard for
// the commonest healthy report there is: no incidents last month, none this
// month. The view model reaches the template as a MAP, so an `omitempty` on
// PreviousIncidentCount dropped that zero out of the map entirely and the
// digest printed "<no value>" where it meant "0" — silently, because a missing
// map key is not an error.
//
// The positive control is the non-zero count in the fixture above, which has
// always rendered.
func TestUptimeReportRendersAZeroPreviousIncidentCount(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	data := fittingMonthlyReport(t)

	data.IncidentCount = 0
	data.LongestIncident = ""
	data.AverageIncident = ""
	data.TotalDowntime = ""
	data.PreviousIncidentCount = 0
	data.ShowIncidentDelta = true
	data.IncidentDeltaText = noChangeText
	data.IncidentDeltaColor = deltaNeutralColor

	html, text := renderReport(t, data)

	for _, body := range []string{html, text} {
		r.NotContains(body, "<no value>")
		r.NotContains(body, "&lt;no value&gt;")
		r.Contains(body, noChangeText)
		r.Contains(body, "June 2026")
	}

	// The zero really is printed as a zero.
	r.Contains(html, "vs June 2026 (0)")
	r.Contains(text, "vs June 2026: 0)")
}
