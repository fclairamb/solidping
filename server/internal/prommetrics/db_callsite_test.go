package prommetrics_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// TestRecordDBQueryCallsiteLabel proves solidping_db_query_duration_seconds
// exposes a distinct series per callsite (uptimebar, in particular — the
// spec's minimum bar) and that repeated observations under the SAME callsite
// (e.g. many requests, many argument values) do not grow the series count:
// cardinality tracks the number of annotated call paths, not traffic.
func TestRecordDBQueryCallsiteLabel(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	reg := prometheus.NewRegistry()
	reg.MustRegister(prommetrics.DBQueryDuration)

	const callsiteName = "db_callsite_test_uptimebar_bucket_availability"

	// Many observations, same callsite — must not multiply the series count.
	for range 50 {
		prommetrics.RecordDBQuery("SELECT", "postgres", callsiteName, 0.01, true)
	}

	// A second, distinct callsite.
	prommetrics.RecordDBQuery("SELECT", "postgres", "db_callsite_test_results_list", 0.02, true)

	// No callsite annotated — the bounded fallback.
	prommetrics.RecordDBQuery("SELECT", "postgres", "unlabelled", 0.03, true)

	families, err := reg.Gather()
	r.NoError(err)

	var dbFamily *dto.MetricFamily
	for _, f := range families {
		if f.GetName() == "solidping_db_query_duration_seconds" {
			dbFamily = f
		}
	}
	r.NotNil(dbFamily, "solidping_db_query_duration_seconds not found")

	seen := make(map[string]int)
	for _, m := range dbFamily.GetMetric() {
		for _, lp := range m.GetLabel() {
			if lp.GetName() == "callsite" {
				seen[lp.GetValue()]++
			}
		}
	}

	// Exactly one series per distinct callsite value recorded above — the 50
	// same-callsite observations landed in the ONE series for that callsite,
	// not 50 series.
	r.Equal(1, seen[callsiteName])
	r.Equal(1, seen["db_callsite_test_results_list"])
	r.Equal(1, seen["unlabelled"])
}
