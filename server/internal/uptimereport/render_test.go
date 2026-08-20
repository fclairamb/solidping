package uptimereport_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/uptimereport"
)

func sampleData() *uptimereport.Data {
	return &uptimereport.Data{
		OrgName:         "acme",
		PeriodLabel:     "July 2026",
		ScopeLabel:      "All checks (2)",
		Timezone:        "Europe/Paris",
		HasData:         true,
		AvailabilityPct: "99.950",
		CheckCount:      2,
		IncidentCount:   3,
		LongestIncident: "42m 0s",
		TotalDowntime:   "1h 5m",
		Checks: []uptimereport.CheckRow{
			{Name: "Production API", HasData: true, AvailabilityPct: "99.980"},
			{Name: "Marketing site", HasData: false},
		},
		SLOs: []uptimereport.SLORow{{
			Name:            "API availability",
			HasData:         true,
			AttainmentPct:   "99.950",
			TargetPct:       "99.900",
			StateLabel:      "Healthy",
			BudgetRemaining: "21m 30s",
		}},
		DashboardURL:   "https://solidping.example/dash0",
		UnsubscribeURL: "https://solidping.example/unsubscribe?token=abc",
	}
}

// roundTrip reproduces what actually happens to the view model in production:
// it is stored in the email job's config, marshalled to JSON, persisted, and
// unmarshalled back into an `any`. html/template therefore sees a map keyed by
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
		r.Contains(body, "42m 0s")
		// Bulk-mail footer.
		r.Contains(body, "https://solidping.example/unsubscribe?token=abc")
	}
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
