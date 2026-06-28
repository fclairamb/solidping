package prommetrics_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

func TestSubsystemCollector(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	reg := prometheus.NewRegistry()

	sizes := prommetrics.SubsystemSizes{
		DEKCacheEntries:  func() int { return 3 },
		RateLimitEntries: func() int { return 7 },
		EventListeners:   func() int { return 2 },
	}
	r.NotPanics(func() {
		prommetrics.RegisterSubsystems(reg, sizes)
	})

	families, err := reg.Gather()
	r.NoError(err)

	values := make(map[string]float64, len(families))
	for _, f := range families {
		require.NotEmpty(t, f.GetMetric())
		values[f.GetName()] = f.GetMetric()[0].GetGauge().GetValue()
	}

	r.InDelta(3.0, values["solidping_dek_cache_entries"], 0.001)
	r.InDelta(7.0, values["solidping_ratelimit_entries"], 0.001)
	r.InDelta(2.0, values["solidping_event_listeners"], 0.001)
	// runtime.NumGoroutine is always > 0 in a running test process.
	r.Greater(values["solidping_runtime_goroutines"], 0.0)
}

// TestSubsystemCollectorNilFuncs verifies nil size functions report 0 rather
// than panicking — the worker-only / partially-initialized case.
func TestSubsystemCollectorNilFuncs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	reg := prometheus.NewRegistry()

	r.NotPanics(func() {
		prommetrics.RegisterSubsystems(reg, prommetrics.SubsystemSizes{})
	})

	families, err := reg.Gather()
	r.NoError(err)

	values := make(map[string]float64, len(families))
	for _, f := range families {
		values[f.GetName()] = f.GetMetric()[0].GetGauge().GetValue()
	}

	r.InDelta(0.0, values["solidping_dek_cache_entries"], 0.001)
	r.InDelta(0.0, values["solidping_ratelimit_entries"], 0.001)
	r.InDelta(0.0, values["solidping_event_listeners"], 0.001)
}
