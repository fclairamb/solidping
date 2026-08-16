package checkprometheus

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ptr is a tiny helper so the samples can express "this threshold IS set"
// (thresholds are pointers precisely so 0 stays a legal value).
func ptr(f float64) *float64 {
	return &f
}

// GetSampleConfigs returns sample Prometheus check configurations: one per
// mode. Both carry an explicit Slug — a sample without one regresses the
// checkdnsbl/checksip default-slug fix.
func (c *PrometheusChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Open file descriptors",
			Slug:   "prometheus-open-fds",
			Period: time.Minute,
			Config: (&PrometheusConfig{
				URL:           "https://app.example.com/metrics",
				Mode:          ModeScrape,
				Metric:        "process_open_fds",
				Operator:      OpGreater,
				WarningValue:  ptr(800),
				CriticalValue: ptr(1000),
			}).GetConfig(),
		},
		{
			Name:   "HTTP 5xx rate",
			Slug:   "prometheus-http-5xx-rate",
			Period: time.Minute,
			Config: (&PrometheusConfig{
				URL:  "https://prometheus.example.com",
				Mode: ModePromQL,
				// promql mode is the escape hatch for counters: rate() is
				// evaluated by Prometheus, not by this checker.
				Query:         `sum(rate(http_requests_total{code=~"5.."}[5m]))`,
				Operator:      OpGreater,
				WarningValue:  ptr(1),
				CriticalValue: ptr(10),
			}).GetConfig(),
		},
	}
}
