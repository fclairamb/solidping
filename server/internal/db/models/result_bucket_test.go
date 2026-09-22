package models

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestResultStatusSetsCoverEveryStatus is the drift guard the two status-set
// helpers promise. They feed SQL predicates in both dialect packages, and the
// bug they exist to prevent is silent: a status added to the ResultStatus block
// but missing from allResultStatuses would simply never appear in
// `status IN (...)`, so the database's availability numbers would diverge from
// uptimebar's Go accumulators with nothing failing to compile.
func TestResultStatusSetsCoverEveryStatus(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// Every code between the lowest and highest declared status must be listed.
	// This is what catches a new constant: ResultStatus is a dense 1..N block,
	// so a gap means either a deliberate hole (there is none today) or an
	// omission.
	seen := make(map[ResultStatus]bool, len(allResultStatuses))
	for _, status := range allResultStatuses {
		r.False(seen[status], "status %d listed twice", status)
		seen[status] = true
	}

	for code := ResultStatusCreated; code <= ResultStatusAbandoned; code++ {
		r.True(seen[code],
			"status %d (%s) is declared but missing from allResultStatuses, so it would "+
				"silently never reach the SQL status predicates",
			code, StatusToString(int(code)))
	}

	// And the two derived sets are exactly the predicates, not a transcription.
	r.Equal([]int{int(ResultStatusUp), int(ResultStatusWarning)}, CountsAsUpStatuses(),
		"up + warning count as success, in column order")
	r.Equal(
		[]int{int(ResultStatusCreated), int(ResultStatusRunning), int(ResultStatusAbandoned)},
		ExcludedFromAvailabilityStatuses(),
		"created/running lifecycle markers plus the reaper's abandoned")

	// The two sets must not overlap: a status that both counts as up and is
	// excluded would make Up > Total possible.
	for _, up := range CountsAsUpStatuses() {
		r.NotContains(ExcludedFromAvailabilityStatuses(), up)
	}
}

// TestResultBucketFilterValidate covers every rejection, and one filter that must
// be accepted so the guards cannot pass by rejecting everything.
func TestResultBucketFilterValidate(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	valid := func() *ResultBucketFilter {
		return &ResultBucketFilter{
			OrganizationUID:  "org",
			CheckUIDs:        []string{"c1"},
			PeriodTypes:      []string{PeriodTypeHour, PeriodTypeDay},
			PeriodStartAfter: time.Now().UTC().Add(-time.Hour),
			BucketDuration:   time.Hour,
		}
	}

	r.NoError(valid().Validate())

	noOrg := valid()
	noOrg.OrganizationUID = ""
	r.ErrorIs(noOrg.Validate(), ErrResultBucketsNoOrganization)

	noWidth := valid()
	noWidth.BucketDuration = 0
	r.ErrorIs(noWidth.Validate(), ErrResultBucketsNoBucketDuration)

	negativeWidth := valid()
	negativeWidth.BucketDuration = -time.Hour
	r.ErrorIs(negativeWidth.Validate(), ErrResultBucketsNoBucketDuration)

	unbounded := valid()
	unbounded.PeriodStartAfter = time.Time{}
	r.ErrorIs(unbounded.Validate(), ErrResultBucketsNoPeriodStart)

	// The important one: a filter straddling the raw/rollup split is implied by
	// neither partial index on `results` and can only be answered by a
	// sequential scan of the largest table in the system.
	mixed := valid()
	mixed.PeriodTypes = []string{PeriodTypeRaw, PeriodTypeHour}
	r.ErrorIs(mixed.Validate(), ErrResultBucketsMixedTier)

	// An empty tier list constrains nothing, so it is mixed too.
	empty := valid()
	empty.PeriodTypes = nil
	r.ErrorIs(empty.Validate(), ErrResultBucketsMixedTier)

	raw := valid()
	raw.PeriodTypes = []string{PeriodTypeRaw}
	r.NoError(raw.Validate())
	r.Equal(PeriodTierRaw, raw.TierSide())
	r.Equal(PeriodTierRollup, valid().TierSide())
}

// TestProlepticEpochOffsetSeconds pins the constant the SQLite bucket expression
// shifts by. It is not an arbitrary number: it is the distance from Go's zero
// time.Time — the origin time.Truncate rounds down to — to the Unix epoch, and a
// wrong value moves every bucket boundary for every width that does not divide
// 24 h.
func TestProlepticEpochOffsetSeconds(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	// time.Time{}.Unix() is the zero time expressed in Unix seconds, i.e. minus
	// the distance we need. (Subtracting the two times directly overflows
	// time.Duration, which tops out around 292 years.)
	r.Equal(int64(ProlepticEpochOffsetSeconds), -time.Time{}.Unix())

	// And the identity the SQL relies on: flooring shifted epoch seconds by the
	// width reproduces time.Truncate, including for a width that does not divide
	// 24 h.
	const width = 7 * time.Hour

	stamp := time.Date(2026, time.September, 22, 13, 47, 19, 0, time.UTC)
	shifted := stamp.Unix() + ProlepticEpochOffsetSeconds
	seconds := int64(width / time.Second)
	binned := time.Unix(shifted/seconds*seconds-ProlepticEpochOffsetSeconds, 0).UTC()

	r.True(stamp.Truncate(width).Equal(binned))

	// Negative control: the epoch origin gets it wrong for this width.
	epochBinned := time.Unix(stamp.Unix()/seconds*seconds, 0).UTC()
	r.False(stamp.Truncate(width).Equal(epochBinned),
		"an epoch-aligned 7h grid must differ, otherwise the offset is untested")
}
