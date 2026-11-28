package checkhost_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhost"
)

// TestRealHost reads the machine running the tests (Linux, macOS and Windows
// CI jobs): every metric must be present and within range.
func TestRealHost(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	cfg := &checkhost.HostConfig{}
	result, err := (&checkhost.HostChecker{}).Execute(t.Context(), cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status, "%v", result.Output)

	for _, name := range []string{
		checkhost.MetricDiskUsedPercent, checkhost.MetricMemoryUsedPercent, checkhost.MetricCPUPercent,
	} {
		value, ok := result.Metrics[name].(float64)
		r.True(ok, name)
		r.GreaterOrEqual(value, 0.0, name)
		r.LessOrEqual(value, 100.0, name)
	}

	for _, name := range []string{checkhost.MetricDiskFreeBytes, checkhost.MetricMemoryAvailableBytes} {
		value, ok := result.Metrics[name].(float64)
		r.True(ok, name)
		r.Positive(value, name)
	}
}
