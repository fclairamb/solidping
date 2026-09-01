package uptimereport_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/uptimereport"
)

func sampleData() *uptimereport.Data {
	return &uptimereport.Data{
		OrgName:           "acme",
		PeriodLabel:       "July 2026",
		ScopeLabel:        "All checks (2)",
		Timezone:          "Europe/Paris",
		HasData:           true,
		AvailabilityPct:   "99.950",
		AvailabilityColor: "#15803d",
		CheckCount:        2,
		IncidentCount:     3,
		LongestIncident:   "42m",
		AverageIncident:   "21m 40s",
		TotalDowntime:     "1h 5m",

		PreviousPeriodLabel:     "June 2026",
		HasPreviousData:         true,
		PreviousAvailabilityPct: "99.870",
		PreviousIncidentCount:   5,
		PreviousAvgResponseTime: "158 ms",

		ShowAvailabilityDelta:  true,
		AvailabilityDeltaText:  "+0.080 pts",
		AvailabilityDeltaColor: "#15803d",

		ShowIncidentDelta:  true,
		IncidentDeltaText:  "-2",
		IncidentDeltaColor: "#15803d",

		ShowResponseDelta:  true,
		ResponseDeltaText:  "+8.4%",
		ResponseDeltaColor: "#b91c1c",

		HasLatency:      true,
		AvgResponseTime: "171 ms",
		MinResponseTime: "42 ms",
		MaxResponseTime: "3.20 s",
		SlowLine:        "4 samples and 2 peaks above 1 s",
		SlowNote:        "A peak is a rolled-up period whose slowest sample exceeded 1 s.",
		LatencyNote:     "Response times include failed samples.",

		DayStripLabel: "Daily availability, 1 Jul – 31 Jul (UTC)",

		Checks: []uptimereport.CheckRow{
			{
				Name: "Production API", HasData: true, AvailabilityPct: "99.980",
				AvailabilityColor: "#15803d",
				URL:               "https://solidping.example/dash0/orgs/acme/checks/chk-prod-api",
				Days: []uptimereport.DayCell{
					{Color: "#15803d", Span: 20, Wide: true},
					{Color: "#b45309", Span: 1},
					{Color: "#15803d", Span: 10, Wide: true},
				},
			},
			{
				Name: "Marketing site", HasData: false,
				URL:  "https://solidping.example/dash0/orgs/acme/checks/chk-marketing",
				Days: []uptimereport.DayCell{{Color: "#d1d5db", Span: 31, Wide: true}},
			},
		},
		SLOs: []uptimereport.SLORow{{
			Name:            "API availability",
			HasData:         true,
			AttainmentPct:   "99.950",
			TargetPct:       "99.900",
			StateLabel:      "Healthy",
			BudgetRemaining: "21m 30s",
			URL:             "https://solidping.example/dash0/orgs/acme/slos/slo-api-availability",
		}},
		DashboardURL:   "https://solidping.example/dash0",
		UnsubscribeURL: "https://solidping.example/unsubscribe?token=abc",
	}
}

// roundTrip reproduces what actually happens to the view model in production:
// it is stored in the email job's config, marshaled to JSON, persisted, and
// unmarshaled back into an `any`. html/template therefore sees a map keyed by
// the JSON TAG, not the Go field name.
//
// Rendering the struct directly would pass even with camelCase tags — which is
// exactly how the blank-digest bug hid — so this test must go through JSON.
func roundTrip(t *testing.T, data *uptimereport.Data) any {
	t.Helper()

	raw, err := json.Marshal(data)
	require.NoError(t, err)

	var out any
	require.NoError(t, json.Unmarshal(raw, &out))

	return out
}

// TestUptimeReportRendersRealContent is the tripwire for the view-model /
// template key contract. It asserts the digest carries actual content — not
// merely that rendering returned no error.
func TestUptimeReportRendersRealContent(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := email.NewFormatter()
	r.NoError(err)

	subject, html, text, err := formatter.Format(uptimereport.TemplateName, roundTrip(t, sampleData()))
	r.NoError(err)

	// A key mismatch renders as the literal "<no value>" rather than failing,
	// so the absence of that string is the single most load-bearing assertion
	// here.
	for _, body := range []string{subject, html, text} {
		r.NotContains(body, "<no value>")
		r.NotContains(body, "&lt;no value&gt;")
	}

	r.Contains(subject, "acme")
	r.Contains(subject, "July 2026")

	for _, body := range []string{html, text} {
		r.Contains(body, "acme")
		r.Contains(body, "July 2026")
		r.Contains(body, "Europe/Paris")
		// HasData true: the overall number renders instead of "No data".
		r.Contains(body, "99.950")
		// The per-check range block really iterated.
		r.Contains(body, "Production API")
		r.Contains(body, "99.980")
		// The per-objective range block really iterated.
		r.Contains(body, "API availability")
		r.Contains(body, "Healthy")
		r.Contains(body, "21m 30s")
		// Incident context.
		r.Contains(body, "42m")
		// Bulk-mail footer.
		r.Contains(body, "https://solidping.example/unsubscribe?token=abc")
		// Check and objective names link to their dash0 pages.
		r.Contains(body, "https://solidping.example/dash0/orgs/acme/checks/chk-prod-api")
		r.Contains(body, "https://solidping.example/dash0/orgs/acme/slos/slo-api-availability")
	}
}

// TestUptimeReportChecksAndObjectivesLinkToTheirDash0Pages pins the HTML
// markup: a check/objective with a URL renders an <a href> around its name,
// and the plain-text part carries the URL as a trailing line rather than
// silently dropping it.
func TestUptimeReportChecksAndObjectivesLinkToTheirDash0Pages(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := email.NewFormatter()
	r.NoError(err)

	_, html, text, err := formatter.Format(uptimereport.TemplateName, roundTrip(t, sampleData()))
	r.NoError(err)

	// The formatter's CSS inliner rewrites `.content a` into an inline
	// style="..." attribute on every <a>, so the tag itself isn't a fixed
	// string — match loosely around the href and the link text instead.
	r.True(linksTo(html, "https://solidping.example/dash0/orgs/acme/checks/chk-prod-api", "Production API"))
	r.True(linksTo(html, "https://solidping.example/dash0/orgs/acme/slos/slo-api-availability", "API availability"))

	// The text part keeps the existing line intact and appends the URL.
	r.Contains(text, "  - Production API: 99.980%\n    https://solidping.example/dash0/orgs/acme/checks/chk-prod-api")
	r.Contains(text, "API availability")
	r.Contains(text, "    https://solidping.example/dash0/orgs/acme/slos/slo-api-availability")
}

// TestUptimeReportRendersPlainNamesWithoutBaseURL is the negative control: no
// URL configured (server.BaseURL empty) must never emit a half-built
// href="" — the name renders as plain text, same as before this change.
func TestUptimeReportRendersPlainNamesWithoutBaseURL(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := email.NewFormatter()
	r.NoError(err)

	data := sampleData()
	for i := range data.Checks {
		data.Checks[i].URL = ""
	}

	for i := range data.SLOs {
		data.SLOs[i].URL = ""
	}

	_, html, text, err := formatter.Format(uptimereport.TemplateName, roundTrip(t, data))
	r.NoError(err)

	r.NotContains(html, `href=""`)
	r.NotContains(html, `<a href="">`)
	r.Contains(html, "Production API")
	r.Contains(html, "API availability")
	r.NotContains(html, `<a href="">Production API</a>`)
	r.NotContains(html, `<a href="">API availability</a>`)
	r.Contains(text, "Production API")
	r.Contains(text, "API availability")
}

// TestUptimeReportColorsAvailabilityByValue pins the value-cell markup: a
// check with data carries an inline color style, a no-data check carries
// none (there is no percentage to color).
func TestUptimeReportColorsAvailabilityByValue(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := email.NewFormatter()
	r.NoError(err)

	_, html, text, err := formatter.Format(uptimereport.TemplateName, roundTrip(t, sampleData()))
	r.NoError(err)

	r.Contains(html, `style="color: #15803d; font-weight: 600">99.980%</span>`)
	// The headline metric carries the same treatment. The CSS inliner merges
	// the .metric-value class rules and this inline color into one style
	// attribute (dropping the space after the colon in the process), so match
	// on the merged declaration rather than a standalone style="...".
	r.Contains(html, "metric-value")
	r.Contains(html, "color:#15803d")
	// The no-data check's value cell carries "No data" honestly, and only the
	// one HasData check row got the colored-value-span treatment — a
	// per-value color span appearing more than once would mean the no-data
	// row got colored too.
	//
	// Counted on the availability-colored VALUE span specifically, not on
	// "font-weight: 600" anywhere: the check-name cells and the trend deltas
	// legitimately carry that weight too.
	r.Contains(html, "No data")
	r.Equal(1, countColoredCheckValues(html, "99.980%"))
	// Color is HTML-only.
	r.NotContains(text, "#15803d")
}

// linksTo reports whether html contains an <a href="url">...text...</a>
// anchor. Loose on attributes between href and the closing bracket, since the
// formatter's CSS inliner adds a style="..." attribute to every anchor.
func linksTo(html, url, text string) bool {
	pattern := fmt.Sprintf(`<a href="%s"[^>]*>%s</a>`, regexp.QuoteMeta(url), regexp.QuoteMeta(text))

	return regexp.MustCompile(pattern).MatchString(html)
}

// The no-data path must say so honestly rather than printing an empty number —
// the positive control above proves the same template can print one.
func TestUptimeReportRendersNoDataHonestly(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := email.NewFormatter()
	r.NoError(err)

	data := sampleData()
	data.HasData = false
	data.AvailabilityPct = ""
	data.Checks = nil
	data.SLOs = nil

	_, html, text, err := formatter.Format(uptimereport.TemplateName, roundTrip(t, data))
	r.NoError(err)

	for _, body := range []string{html, text} {
		r.NotContains(body, "<no value>")
		r.Contains(body, "No data")
		// The header still renders, so "No data" is a statement about the
		// window and not a symptom of a blank view model.
		r.Contains(body, "acme")
		r.Contains(body, "July 2026")
		// The range blocks are genuinely empty now.
		r.NotContains(body, "Production API")
		r.NotContains(body, "API availability")
	}
}

// TestUptimeReportRendersAsAStructDirectly is the struct-view-model guard the
// branding work needs (see supportreply.go and formatter.go's field() helper):
// html/template ERRORS on a missing STRUCT field, and uptimereport.Data is the
// repo's real struct view model. Rendering it *without* the JSON round trip is
// therefore the strictest form of the check — if base.html ever goes back to a
// direct `.OrgLogoURL` / `.UnsubscribeCheckName` access, this fails.
func TestUptimeReportRendersAsAStructDirectly(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := email.NewFormatter(email.WithBaseURL("https://solidping.example"))
	r.NoError(err)

	data := sampleData()
	data.BrandName = data.OrgName
	data.OrgLogoURL = "/pub/assets/org-logo-uid"

	_, html, text, err := formatter.Format(uptimereport.TemplateName, data)
	r.NoError(err)

	// Branding really landed, so a passing test cannot mean "the header was
	// skipped entirely".
	r.Contains(html, "https://solidping.example/pub/assets/org-logo-uid")
	r.Contains(html, "acme — sent by SolidPing")
	r.NotContains(html, "<no value>")
	r.NotContains(text, "<no value>")
}

// TestUptimeReportBrandingSurvivesTheJSONRoundTrip pins the same for the path
// production actually takes — the view model is persisted as JSON on the email
// job and read back as a map. A camelCase tag on the two new branding fields
// would silently unbrand every digest, exactly like the OrgName bug this file
// already guards against.
func TestUptimeReportBrandingSurvivesTheJSONRoundTrip(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	formatter, err := email.NewFormatter(email.WithBaseURL("https://solidping.example"))
	r.NoError(err)

	data := sampleData()
	data.BrandName = data.OrgName
	data.OrgLogoURL = "/pub/assets/org-logo-uid"

	_, html, _, err := formatter.Format(uptimereport.TemplateName, roundTrip(t, data))
	r.NoError(err)

	r.Contains(html, "https://solidping.example/pub/assets/org-logo-uid")
	r.Contains(html, `alt="acme"`)
}

// countColoredCheckValues counts value spans rendered with an interpolated
// availability color around the given text.
func countColoredCheckValues(html, value string) int {
	pattern := fmt.Sprintf(`style="color: #[0-9a-f]{6}; font-weight: 600">%s</span>`, regexp.QuoteMeta(value))

	return len(regexp.MustCompile(pattern).FindAllString(html, -1))
}
