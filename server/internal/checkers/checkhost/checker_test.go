package checkhost_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhost"
)

func TestGrading(t *testing.T) {
	t.Parallel()

	allThresholds := map[string]any{
		"diskWarningPercent": 80, "diskCriticalPercent": 90,
		"memoryWarningPercent": 70, "memoryCriticalPercent": 85,
		"cpuWarningPercent": 60, "cpuCriticalPercent": 95,
	}

	tests := []struct {
		name     string
		sampler  [3]float64
		cfg      map[string]any
		status   checkerdef.Status
		breaches []string
	}{
		{"all below", [3]float64{10, 10, 10}, allThresholds, checkerdef.StatusUp, nil},
		{"warning only", [3]float64{85, 10, 10}, allThresholds, checkerdef.StatusWarning, []string{"disk"}},
		{"equal is a breach", [3]float64{90, 10, 10}, allThresholds, checkerdef.StatusDown, []string{"disk"}},
		{
			"critical wins over warning across metrics",
			[3]float64{85, 10, 99},
			allThresholds,
			checkerdef.StatusDown,
			[]string{"disk", "cpu"},
		},
		{"no threshold", [3]float64{99, 99, 99}, map[string]any{}, checkerdef.StatusUp, nil},
		{
			"warning only config never pages",
			[3]float64{99, 10, 10},
			map[string]any{"diskWarningPercent": 50},
			checkerdef.StatusWarning,
			[]string{"disk"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			sampler := &fakeSampler{diskUsed: test.sampler[0], memUsed: test.sampler[1], cpu: test.sampler[2]}
			result := run(t, t.Context(), sampler, test.cfg)
			r.Equal(test.status, result.Status)

			// Every metric is stored, thresholds or not.
			for _, name := range []string{
				checkhost.MetricDiskUsedPercent, checkhost.MetricDiskFreeBytes,
				checkhost.MetricMemoryUsedPercent, checkhost.MetricMemoryAvailableBytes, checkhost.MetricCPUPercent,
			} {
				r.Contains(result.Metrics, name)
			}

			r.InDelta(test.sampler[0], result.Metrics[checkhost.MetricDiskUsedPercent], 0.001)
			r.InDelta(1000.0, result.Metrics[checkhost.MetricDiskFreeBytes], 0.001)

			if len(test.breaches) == 0 {
				r.NotContains(result.Output, checkhost.OutputKeyBreaches)
			}

			for _, name := range test.breaches {
				r.Contains(result.Output[checkerdef.OutputKeyMessage], name)
			}
		})
	}
}

func TestFailingSourceIsAnError(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom") //nolint:err113 // test fixture

	for name, sampler := range map[string]*fakeSampler{
		"disk":   {diskErr: boom},
		"memory": {memErr: boom},
		"cpu":    {cpuEr: boom},
	} {
		result := run(t, t.Context(), sampler, map[string]any{"diskCriticalPercent": 90})
		require.Equal(t, checkerdef.StatusError, result.Status, name)
		require.Contains(t, result.Output[checkerdef.OutputKeyError], name)
		require.Contains(t, result.Output[checkerdef.OutputKeyError], "boom")
	}
}
