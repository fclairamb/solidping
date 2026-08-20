package uptimebar

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// rawRowMaintenance builds a raw row recorded while an active maintenance
// window covered the check (spec 2026-08-20-01).
func rawRowMaintenance(
	checkUID string, status models.ResultStatus, start time.Time, dur float32,
) *models.Result {
	row := rawRow(checkUID, status, start, dur)
	row.Maintenance = true

	return row
}

// hourRowMaintenance builds an aggregated row carrying the maintenance subset.
func hourRowMaintenance(
	checkUID string, total, success, maintTotal, maintSuccess int, start time.Time,
) *models.Result {
	row := hourRow(checkUID, total, success, start)
	row.MaintenanceChecks = &maintTotal
	row.MaintenanceSuccessfulChecks = &maintSuccess

	return row
}

// TestBucketAvailability_MaintenanceDoesNotMoveRawAvailability is the guard for
// spec 2026-08-20-01's "no behavior change outside SLOs" promise.
//
// Status pages, badges and the availability API all read AvailabilityPct off
// these buckets. Maintenance tagging must therefore be strictly additive: the
// tagged and untagged fixtures below differ ONLY in the flag, and must produce
// byte-identical Up/Total.
//
// The positive control is ExcludingMaintenance(), which DOES move the number —
// without it, an assertion that "the percentage is unchanged" would also pass
// if the counters never reached the bucket at all.
func TestBucketAvailability_MaintenanceDoesNotMoveRawAvailability(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)
	bucketStart := currentHour.Add(-23 * time.Hour)

	// Four probes; the two failures happen to fall inside a maintenance window.
	tagged := &fakeLister{results: []*models.Result{
		rawRow("c1", models.ResultStatusUp, currentHour.Add(1*time.Minute), 40),
		rawRow("c1", models.ResultStatusUp, currentHour.Add(2*time.Minute), 40),
		rawRowMaintenance("c1", models.ResultStatusDown, currentHour.Add(3*time.Minute), 0),
		rawRowMaintenance("c1", models.ResultStatusDown, currentHour.Add(4*time.Minute), 0),
	}}

	// The identical window with nothing tagged.
	untagged := &fakeLister{results: []*models.Result{
		rawRow("c1", models.ResultStatusUp, currentHour.Add(1*time.Minute), 40),
		rawRow("c1", models.ResultStatusUp, currentHour.Add(2*time.Minute), 40),
		rawRow("c1", models.ResultStatusDown, currentHour.Add(3*time.Minute), 0),
		rawRow("c1", models.ResultStatusDown, currentHour.Add(4*time.Minute), 0),
	}}

	taggedOut, err := BucketAvailability(
		context.Background(), tagged, "org", []string{"c1"}, time.Hour, bucketStart, 24, noHints(),
	)
	r.NoError(err)

	untaggedOut, err := BucketAvailability(
		context.Background(), untagged, "org", []string{"c1"}, time.Hour, bucketStart, 24, noHints(),
	)
	r.NoError(err)

	taggedStats := taggedOut["c1"][currentHour]
	untaggedStats := untaggedOut["c1"][currentHour]

	r.Equal(untaggedStats.Up, taggedStats.Up)
	r.Equal(untaggedStats.Total, taggedStats.Total)

	pct, ok := taggedStats.AvailabilityPct()
	r.True(ok)
	r.InDelta(50.0, pct, 0.0001, "raw availability must still see the maintenance failures")

	// The subset counters DID arrive — otherwise the equality above would be
	// vacuous.
	r.Equal(2, taggedStats.MaintTotal)
	r.Zero(taggedStats.MaintUp)
	r.Zero(untaggedStats.MaintTotal)

	// Positive control: only the SLO read path subtracts, and when it does the
	// number really moves.
	excluded := taggedStats.ExcludingMaintenance()
	r.Equal(2, excluded.Total)
	r.Equal(2, excluded.Up)

	excludedPct, ok := excluded.AvailabilityPct()
	r.True(ok)
	r.InDelta(100.0, excludedPct, 0.0001)
}

// Same guarantee one tier up: an aggregated row's maintenance subset must not
// be folded into total_checks / successful_checks on the way into a bucket.
func TestBucketAvailability_MaintenanceSubsetIsNotDoubleCountedFromRollups(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	now := time.Now().UTC()
	currentHour := now.Truncate(time.Hour)
	bucketStart := currentHour.Add(-23 * time.Hour)
	oldHour := currentHour.Add(-5 * time.Hour)

	lister := &fakeLister{results: []*models.Result{
		hourRowMaintenance("c1", 100, 90, 20, 15, oldHour),
	}}

	out, err := BucketAvailability(
		context.Background(), lister, "org", []string{"c1"}, time.Hour, bucketStart, 24, hints(),
	)
	r.NoError(err)

	stats := out["c1"][oldHour]
	r.Equal(100, stats.Total, "the subset must not be added on top of total_checks")
	r.Equal(90, stats.Up, "the subset must not be added on top of successful_checks")
	r.Equal(20, stats.MaintTotal)
	r.Equal(15, stats.MaintUp)

	excluded := stats.ExcludingMaintenance()
	r.Equal(80, excluded.Total)
	r.Equal(75, excluded.Up)
}

// BucketStats.Add is the single merge point every group-level surface goes
// through (statuspages.mergeBuckets, slo.MergeStats). It must keep the
// maintenance subset in its OWN fields — folding it into Up/Total there would
// silently change every status page's group availability.
func TestBucketStatsAddKeepsMaintenanceSubsetSeparate(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	var merged BucketStats
	merged.Add(BucketStats{Up: 90, Total: 100, MaintUp: 5, MaintTotal: 10, DurCnt: 100, DurSum: 500})
	merged.Add(BucketStats{Up: 50, Total: 100, MaintUp: 0, MaintTotal: 0, DurCnt: 100, DurSum: 300})

	r.Equal(140, merged.Up)
	r.Equal(200, merged.Total)
	r.Equal(5, merged.MaintUp)
	r.Equal(10, merged.MaintTotal)

	pct, ok := merged.AvailabilityPct()
	r.True(ok)
	r.InDelta(70.0, pct, 0.0001, "the merged raw availability must ignore the maintenance subset")

	// Positive control: the subset really is carried, and subtracting it moves
	// the number.
	excludedPct, ok := merged.ExcludingMaintenance().AvailabilityPct()
	r.True(ok)
	r.InDelta(71.0526, excludedPct, 0.001)
}
