package prommetrics

import (
	"database/sql"
	"errors"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// dbStatsCollector exposes database/sql.DB.Stats() as Prometheus gauges.
// Implementing Collector (rather than caching gauges) means Stats() is
// called only when /metrics is scraped — zero cost at idle.
type dbStatsCollector struct {
	db      *sql.DB
	backend string

	maxOpen           *prometheus.Desc
	open              *prometheus.Desc
	inUse             *prometheus.Desc
	idle              *prometheus.Desc
	waitCount         *prometheus.Desc
	waitDuration      *prometheus.Desc
	maxIdleClosed     *prometheus.Desc
	maxLifetimeClosed *prometheus.Desc
}

// NewDBStatsCollector returns a Prometheus collector that emits the
// fields of sql.DBStats as gauges labeled with the given backend
// ("sqlite" or "postgres").
func NewDBStatsCollector(db *sql.DB, backend string) prometheus.Collector {
	labels := prometheus.Labels{labelBackend: backend}
	desc := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc("solidping_db_pool_"+name, help, nil, labels)
	}
	return &dbStatsCollector{
		db:                db,
		backend:           backend,
		maxOpen:           desc("max_open_connections", "Maximum number of open connections configured on the pool"),
		open:              desc("open_connections", "Number of established connections both in-use and idle"),
		inUse:             desc("in_use_connections", "Number of connections currently in use"),
		idle:              desc("idle_connections", "Number of idle connections"),
		waitCount:         desc("wait_count_total", "Total number of connections waited for"),
		waitDuration:      desc("wait_duration_seconds_total", "Cumulative time blocked waiting for a connection"),
		maxIdleClosed:     desc("max_idle_closed_total", "Total connections closed due to SetMaxIdleConns"),
		maxLifetimeClosed: desc("max_lifetime_closed_total", "Total connections closed due to SetConnMaxLifetime"),
	}
}

// Describe implements prometheus.Collector. The `ch` parameter name
// matches the upstream Collector interface signature.
//
//nolint:varnamelen // ch is the conventional name for the Collector channel
func (c *dbStatsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.maxOpen
	ch <- c.open
	ch <- c.inUse
	ch <- c.idle
	ch <- c.waitCount
	ch <- c.waitDuration
	ch <- c.maxIdleClosed
	ch <- c.maxLifetimeClosed
}

// Collect implements prometheus.Collector.
//
//nolint:varnamelen // ch is the conventional name for the Collector channel
func (c *dbStatsCollector) Collect(ch chan<- prometheus.Metric) {
	stats := c.db.Stats()
	ch <- prometheus.MustNewConstMetric(c.maxOpen, prometheus.GaugeValue, float64(stats.MaxOpenConnections))
	ch <- prometheus.MustNewConstMetric(c.open, prometheus.GaugeValue, float64(stats.OpenConnections))
	ch <- prometheus.MustNewConstMetric(c.inUse, prometheus.GaugeValue, float64(stats.InUse))
	ch <- prometheus.MustNewConstMetric(c.idle, prometheus.GaugeValue, float64(stats.Idle))
	ch <- prometheus.MustNewConstMetric(c.waitCount, prometheus.CounterValue, float64(stats.WaitCount))
	ch <- prometheus.MustNewConstMetric(c.waitDuration, prometheus.CounterValue, stats.WaitDuration.Seconds())
	ch <- prometheus.MustNewConstMetric(c.maxIdleClosed, prometheus.CounterValue, float64(stats.MaxIdleClosed))
	ch <- prometheus.MustNewConstMetric(c.maxLifetimeClosed, prometheus.CounterValue, float64(stats.MaxLifetimeClosed))
}

// dbRegistered guards against duplicate registration when the same
// backend label is wired up more than once (e.g. tests opening a fresh
// DB after the first one was already registered). Package-level state
// is intentional: the Prometheus default registry is itself a singleton.
//
//nolint:gochecknoglobals // mirrors the default Prometheus registry singleton
var (
	dbRegisteredMu sync.Mutex
	dbRegistered   = map[string]bool{}
)

// RegisterDB registers a DB-pool stats collector for the given backend with
// the default Prometheus registerer. Safe to call multiple times for the
// same backend label: a duplicate registration is silently ignored so
// embedded-PG bootstraps in tests don't panic.
func RegisterDB(db *sql.DB, backend string) {
	registerDB(prometheus.DefaultRegisterer, db, backend)
}

func registerDB(reg prometheus.Registerer, db *sql.DB, backend string) {
	dbRegisteredMu.Lock()
	defer dbRegisteredMu.Unlock()
	if dbRegistered[backend] {
		return
	}
	c := NewDBStatsCollector(db, backend)
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if !errors.As(err, &are) {
			panic(err)
		}
	}
	dbRegistered[backend] = true
}
