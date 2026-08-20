package models

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResultStatusIsLifecycleMarker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status ResultStatus
		want   bool
	}{
		{WireStatusCreated, ResultStatusCreated, true},
		{WireStatusRunning, ResultStatusRunning, true},
		{WireStatusUp, ResultStatusUp, false},
		{WireStatusDown, ResultStatusDown, false},
		{WireStatusTimeout, ResultStatusTimeout, false},
		{WireStatusError, ResultStatusError, false},
		{WireStatusDegraded, ResultStatusDegraded, false},
		{WireStatusWarning, ResultStatusWarning, false},
		// Abandoned is terminal, not a lifecycle marker — it is excluded from
		// availability by ExcludedFromAvailability's other arm, not this one.
		{WireStatusAbandoned, ResultStatusAbandoned, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.New(t).Equal(tt.want, tt.status.IsLifecycleMarker())
		})
	}
}

func TestResultStatusCountsAsUp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status ResultStatus
		want   bool
	}{
		{WireStatusCreated, ResultStatusCreated, false},
		{WireStatusRunning, ResultStatusRunning, false},
		{WireStatusUp, ResultStatusUp, true},
		{WireStatusDown, ResultStatusDown, false},
		{WireStatusTimeout, ResultStatusTimeout, false},
		{WireStatusError, ResultStatusError, false},
		// Degraded is an aggregated rollup status, never on a raw row; it does
		// not count as up for raw availability.
		{WireStatusDegraded, ResultStatusDegraded, false},
		// Warning is "up with something to report" — counts as up.
		{WireStatusWarning, ResultStatusWarning, true},
		// Abandoned is not up. It is not down either — it is excluded from the
		// denominator entirely, which is ExcludedFromAvailability's job.
		{WireStatusAbandoned, ResultStatusAbandoned, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.New(t).Equal(tt.want, tt.status.CountsAsUp())
		})
	}
}

// TestStatusToStringCoversAbandoned pins the wire/diagnostic name of the
// reaper-minted status. incidents' resultSnapshot stores this string on an
// incident, where it outlives the raw row itself, so an unmapped 9 would
// silently become "UNKNOWN" in the only surviving record of what happened.
func TestStatusToStringCoversAbandoned(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal("ABANDONED", StatusToString(int(ResultStatusAbandoned)))
	// Controls: the neighboring values are unchanged, and an unknown value
	// still falls through.
	r.Equal("ERROR", StatusToString(int(ResultStatusError)))
	r.Equal("WARNING", StatusToString(int(ResultStatusWarning)))
	r.Equal("UNKNOWN", StatusToString(99))
}

func TestRawAvailability(t *testing.T) {
	t.Parallel()

	up := int(ResultStatusUp)
	down := int(ResultStatusDown)
	created := int(ResultStatusCreated)
	running := int(ResultStatusRunning)
	warning := int(ResultStatusWarning)
	timeout := int(ResultStatusTimeout)
	errored := int(ResultStatusError)
	abandoned := int(ResultStatusAbandoned)

	tests := []struct {
		name        string
		results     []*Result
		wantSuccess int
		wantTotal   int
	}{
		{
			name:        "empty",
			results:     nil,
			wantSuccess: 0,
			wantTotal:   0,
		},
		{
			name: "lifecycle rows excluded from denominator",
			// 18 up + 2 created + 1 running → success 18, total 18.
			results: append(append(
				repeatStatus(up, 18),
				repeatStatus(created, 2)...),
				repeatStatus(running, 1)...),
			wantSuccess: 18,
			wantTotal:   18,
		},
		{
			name:        "warning counts as up",
			results:     []*Result{{Status: &up}, {Status: &warning}, {Status: &up}},
			wantSuccess: 3,
			wantTotal:   3,
		},
		{
			name:        "down and timeout count against availability",
			results:     []*Result{{Status: &up}, {Status: &down}, {Status: &timeout}, {Status: &up}},
			wantSuccess: 2,
			wantTotal:   4,
		},
		{
			name:        "nil status ignored",
			results:     []*Result{{Status: nil}, {Status: &up}, {Status: nil}},
			wantSuccess: 1,
			wantTotal:   1,
		},
		{
			name:        "only lifecycle rows → total zero",
			results:     []*Result{{Status: &created}, {Status: &running}},
			wantSuccess: 0,
			wantTotal:   0,
		},
		{
			// Positive control for the reaper's exclusion rule: a genuine
			// error still counts against availability — only
			// ResultStatusAbandoned is excluded (specs 2026-08-18-03 /
			// 2026-08-18-10). Both rows are terminal failures on the wire;
			// only the status tells them apart.
			name: "reaped result excluded, genuine error still counts",
			results: []*Result{
				{Status: &up},
				{Status: &abandoned}, // reaped: excluded entirely
				{Status: &errored},   // genuine error: counts against availability
			},
			wantSuccess: 1,
			wantTotal:   2,
		},
		{
			// The edge the exclusion rule creates: a window of nothing but
			// reaped attempts is "no data", never a manufactured 0% outage.
			name:        "only abandoned rows → total zero",
			results:     []*Result{{Status: &abandoned}, {Status: &abandoned}},
			wantSuccess: 0,
			wantTotal:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			success, total := RawAvailability(tt.results)
			r.Equal(tt.wantSuccess, success, "success count")
			r.Equal(tt.wantTotal, total, "countable total")
		})
	}
}

func repeatStatus(status int, n int) []*Result {
	out := make([]*Result, 0, n)
	for i := 0; i < n; i++ {
		s := status
		out = append(out, &Result{Status: &s})
	}
	return out
}

func TestResultExcludedFromAvailability(t *testing.T) {
	t.Parallel()

	up := int(ResultStatusUp)
	created := int(ResultStatusCreated)
	running := int(ResultStatusRunning)
	errored := int(ResultStatusError)
	warning := int(ResultStatusWarning)
	abandoned := int(ResultStatusAbandoned)

	tests := []struct {
		name   string
		result *Result
		want   bool
	}{
		{"up", &Result{Status: &up}, false},
		{"created marker", &Result{Status: &created}, true},
		{"running marker", &Result{Status: &running}, true},
		{"nil status", &Result{Status: nil}, false},
		{"abandoned", &Result{Status: &abandoned}, true},
		// The two negative controls that matter: neither a genuine error nor
		// a warning may be swept up by the abandoned exclusion.
		{"genuine error still counts", &Result{Status: &errored}, false},
		{"warning still counts", &Result{Status: &warning}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.New(t).Equal(tt.want, tt.result.ExcludedFromAvailability())
		})
	}
}

// TestResultStatusExcludedFromAvailability covers the status-level predicate
// the three availability surfaces (hour rollup, uptimebar union, availability
// service) share. It is the whole enum, on purpose: the exclusion set must
// stay exactly {created, running, abandoned} — any other status silently
// joining it would remove real measurements from every customer's uptime.
func TestResultStatusExcludedFromAvailability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status ResultStatus
		want   bool
	}{
		{WireStatusCreated, ResultStatusCreated, true},
		{WireStatusRunning, ResultStatusRunning, true},
		{WireStatusAbandoned, ResultStatusAbandoned, true},
		{WireStatusUp, ResultStatusUp, false},
		{WireStatusDown, ResultStatusDown, false},
		{WireStatusTimeout, ResultStatusTimeout, false},
		{WireStatusError, ResultStatusError, false},
		{WireStatusDegraded, ResultStatusDegraded, false},
		{WireStatusWarning, ResultStatusWarning, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.New(t).Equal(tt.want, tt.status.ExcludedFromAvailability())
		})
	}
}

func TestAbandonedResultThreshold(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// threshold = multiplier * (period + leaseGrace)
	r.Equal(
		AbandonedResultMultiplier*(time.Minute+AbandonedResultLeaseGrace),
		AbandonedResultThreshold(time.Minute),
	)

	// A longer period yields a longer threshold — monotonic in period.
	r.Greater(AbandonedResultThreshold(time.Hour), AbandonedResultThreshold(time.Minute))
}
