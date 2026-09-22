package jobtypes

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// TestRawMetricsP95UsesTheSharedNearestRankIndex is the join between the
// aggregation job's duration_p95 and the status page's SQL-binned seam.
//
// The job picks its p95 by SORTING the bucket's durations and taking the sample at
// `int(float64(n) * 0.95)`, clamped. The seam aggregate cannot call that function —
// it runs in the database — so it selects the sample at rank
// `(n * 19) / 20 + 1` (models.ResponseTimeBinP95Index + 1), integer arithmetic in
// both dialects. This test is what makes "the same number" a fact rather than a
// claim: for every bucket size from 1 to 5 000 it feeds calculateRawMetrics a set
// of DISTINCT durations, so the value it returns identifies the index it chose,
// and requires that index to be models.ResponseTimeBinP95Index(n).
//
// Without this, the two could drift in the one way that would matter and would not
// be visible: a seam point and the hour rollup that replaces it disagreeing by one
// sample, which on a chart reads as the series stepping whenever the aggregation
// job runs. It also covers the float-vs-integer hazard — `n * 0.95` in IEEE doubles
// can land a hair below a whole number, which shifts the index down by one for
// exactly the sizes where n is a multiple of 20.
func TestRawMetricsP95UsesTheSharedNearestRankIndex(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	for n := 1; n <= 5_000; n++ {
		durations := make([]float32, 0, n)

		var total float32

		for i := range n {
			// Distinct, strictly increasing, and exactly representable in float32 so
			// the returned value maps back to one index with no ambiguity.
			duration := float32(i + 1)
			durations = append(durations, duration)
			total += duration
		}

		_, p95 := calculateRawMetrics(durations, total)

		wantIndex := models.ResponseTimeBinP95Index(n)
		r.InDelta(float32(wantIndex+1), p95, 0.0001,
			"n=%d: the job picked sample %v, models.ResponseTimeBinP95Index says index %d",
			n, p95, wantIndex)
	}
}

// TestResponseTimeBinP95IndexIsInRange pins the two edges the SQL relies on: the
// index is never negative and never past the last sample, so
// `rn = (cnt * 19) / 20 + 1` always matches exactly one row of a non-empty
// partition (a rank beyond cnt would match none and the bin would come back with a
// NULL p95 despite having durations).
func TestResponseTimeBinP95IndexIsInRange(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Zero(models.ResponseTimeBinP95Index(0), "an empty set has no rank to pick")

	for n := 1; n <= 10_000; n++ {
		index := models.ResponseTimeBinP95Index(n)
		r.GreaterOrEqual(index, 0, "n=%d", n)
		r.Less(index, n, "n=%d: rank %d would match no row", n, index+1)
	}
}
