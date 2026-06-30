package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResultStatusIsLifecycleMarker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status ResultStatus
		want   bool
	}{
		{"created", ResultStatusCreated, true},
		{"running", ResultStatusRunning, true},
		{"up", ResultStatusUp, false},
		{"down", ResultStatusDown, false},
		{"timeout", ResultStatusTimeout, false},
		{"error", ResultStatusError, false},
		{"degraded", ResultStatusDegraded, false},
		{"warning", ResultStatusWarning, false},
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
		{"created", ResultStatusCreated, false},
		{"running", ResultStatusRunning, false},
		{"up", ResultStatusUp, true},
		{"down", ResultStatusDown, false},
		{"timeout", ResultStatusTimeout, false},
		{"error", ResultStatusError, false},
		// Degraded is an aggregated rollup status, never on a raw row; it does
		// not count as up for raw availability.
		{"degraded", ResultStatusDegraded, false},
		// Warning is "up with something to report" — counts as up.
		{"warning", ResultStatusWarning, true},
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
