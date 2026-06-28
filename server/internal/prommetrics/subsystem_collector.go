package prommetrics

import (
	"errors"
	"runtime"

	"github.com/prometheus/client_golang/prometheus"
)

// SubsystemSizes provides scrape-time counts for the long-lived in-memory
// structures whose growth go_memstats cannot attribute. Closures are injected
// at registration so prommetrics stays free of import cycles
// (middleware/credentials/notifier all import nothing from here).
//
// Any function may be nil; a nil function is reported as 0. NumGoroutine is
// always sourced from the runtime directly and cross-checks the Go collector's
// go_goroutines.
type SubsystemSizes struct {
	// DEKCacheEntries returns the per-org DEK cache size (grows O(orgs), never
	// evicted).
	DEKCacheEntries func() int
	// RateLimitEntries returns the per-IP rate-limiter map size (grows O(unique
	// IPs); pruned by a cleanup loop).
	RateLimitEntries func() int
	// EventListeners returns the total registered notifier listener channels
	// (channel-per-Listen; leaks if not deregistered).
	EventListeners func() int
}

// subsystemCollector emits the SubsystemSizes counts as gauges, reading them at
// scrape time (zero idle cost) — the same pattern as dbStatsCollector.
type subsystemCollector struct {
	sizes SubsystemSizes

	dekCache   *prometheus.Desc
	rateLimit  *prometheus.Desc
	listeners  *prometheus.Desc
	goroutines *prometheus.Desc
}

// NewSubsystemCollector returns a Prometheus collector exposing the suspect
// subsystem sizes plus runtime.NumGoroutine.
func NewSubsystemCollector(sizes SubsystemSizes) prometheus.Collector {
	desc := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc(name, help, nil, nil)
	}
	return &subsystemCollector{
		sizes:      sizes,
		dekCache:   desc("solidping_dek_cache_entries", "Number of per-org DEKs cached in memory (grows O(orgs), never evicted)"),
		rateLimit:  desc("solidping_ratelimit_entries", "Number of per-IP entries in the HTTP rate-limiter map (pruned by cleanup loop)"),
		listeners:  desc("solidping_event_listeners", "Total registered local/pg event-notifier listener channels"),
		goroutines: desc("solidping_runtime_goroutines", "runtime.NumGoroutine() — cross-checks go_goroutines"),
	}
}

// Describe implements prometheus.Collector.
//
//nolint:varnamelen // ch is the conventional name for the Collector channel
func (c *subsystemCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.dekCache
	ch <- c.rateLimit
	ch <- c.listeners
	ch <- c.goroutines
}

// Collect implements prometheus.Collector.
//
//nolint:varnamelen // ch is the conventional name for the Collector channel
func (c *subsystemCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(c.dekCache, prometheus.GaugeValue, callOrZero(c.sizes.DEKCacheEntries))
	ch <- prometheus.MustNewConstMetric(c.rateLimit, prometheus.GaugeValue, callOrZero(c.sizes.RateLimitEntries))
	ch <- prometheus.MustNewConstMetric(c.listeners, prometheus.GaugeValue, callOrZero(c.sizes.EventListeners))
	ch <- prometheus.MustNewConstMetric(c.goroutines, prometheus.GaugeValue, float64(runtime.NumGoroutine()))
}

// callOrZero invokes fn and returns its result as a float64, or 0 if fn is nil.
func callOrZero(fn func() int) float64 {
	if fn == nil {
		return 0
	}
	return float64(fn())
}

// RegisterSubsystems registers a subsystem-sizes collector with reg. Safe to
// call once per process; a duplicate registration is ignored so tests that
// re-bootstrap don't panic.
func RegisterSubsystems(reg prometheus.Registerer, sizes SubsystemSizes) {
	c := NewSubsystemCollector(sizes)
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			panic(err)
		}
	}
}
