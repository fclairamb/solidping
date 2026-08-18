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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.New(t).Equal(tt.want, tt.status.CountsAsUp())
		})
	}
}

func TestRawAvailability(t *testing.T) {
	t.Parallel()

	up := int(ResultStatusUp)
	down := int(ResultStatusDown)
	created := int(ResultStatusCreated)
	running := int(ResultStatusRunning)
	warning := int(ResultStatusWarning)
	timeout := int(ResultStatusTimeout)

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
			// error (status=error, Abandoned=false) still counts against
			// availability — only Abandoned=true is excluded (spec
			// 2026-08-18-03).
			name: "reaped result excluded, genuine error still counts",
			results: []*Result{
				{Status: &up},
				{Status: &down, Abandoned: true}, // reaped: excluded entirely
				{Status: &down},                  // genuine error: counts against availability
			},
			wantSuccess: 1,
			wantTotal:   2,
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

	tests := []struct {
		name   string
		result *Result
		want   bool
	}{
		{"up, not abandoned", &Result{Status: &up}, false},
		{"created marker", &Result{Status: &created}, true},
		{"nil status, not abandoned", &Result{Status: nil}, false},
		{"up but abandoned", &Result{Status: &up, Abandoned: true}, true},
		{"nil status and abandoned", &Result{Status: nil, Abandoned: true}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.New(t).Equal(tt.want, tt.result.ExcludedFromAvailability())
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
