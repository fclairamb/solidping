# Prometheus Metrics Endpoint

## Overview

SolidPing already emits OpenTelemetry metrics (duration histogram, execution counter, failed execution counter) via OTLP export. However, many operators run Prometheus as their metrics backend and expect a scrapeable `/metrics` endpoint. This spec adds native Prometheus exposition so SolidPing can be monitored without requiring an OTel Collector as intermediary.

**Use cases:**
- Operators scrape `/metrics` directly from SolidPing into Prometheus/VictoriaMetrics/Mimir
- Grafana dashboards for check health, worker capacity, and incident rates
- Alertmanager rules on SolidPing internals (e.g., worker pool exhaustion, scheduling lag)
- Capacity planning based on check volume and execution latency trends

**Design decisions:**
- **Dedicated `/metrics` endpoint** on the main HTTP server (same port as API)
- **Coexist with OTel**: Prometheus metrics are registered independently — OTel export remains unchanged
- **Cardinality control**: Labels kept to `check_type`, `status`, `region`, `organization` — no per-check-UID labels on hot-path counters to avoid cardinality explosion. Per-check gauges (status, availability) use `check_slug` since their cardinality is bounded by check count.
- **Pull model**: No push gateway — Prometheus scrapes the endpoint directly
- **Standard library**: Use `prometheus/client_golang` (the de facto Go Prometheus client)

---

## Metrics

### Check Execution (hot path — incremented on every check run)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `solidping_check_executions_total` | Counter | `check_type`, `status`, `region`, `organization` | Total check executions |
| `solidping_check_duration_milliseconds` | Histogram | `check_type`, `status`, `region`, `organization` | Check execution duration. Buckets: 1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000 ms |
| `solidping_check_scheduling_delay_seconds` | Histogram | `region` | Delay between scheduled time and actual execution start. Buckets: 0.1, 0.5, 1, 2, 5, 10, 30, 60 s |

### Check Status (per-check gauges — bounded by check count)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `solidping_check_up` | Gauge | `check_slug`, `check_type`, `region`, `organization` | 1 if check is currently UP, 0 otherwise |
| `solidping_check_status_streak` | Gauge | `check_slug`, `check_type`, `organization` | Consecutive results with current status (useful for flap detection) |

### Inventory (low-frequency — updated on check create/update/delete)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `solidping_checks_configured_total` | Gauge | `check_type`, `organization`, `enabled` | Number of configured checks by type and enabled state |

### Workers

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `solidping_workers_active` | Gauge | `region` | Number of workers with heartbeat within last 2 minutes |
| `solidping_worker_free_runners` | Gauge | `worker_uid`, `region` | Available runner slots per worker |
| `solidping_worker_jobs_claimed_total` | Counter | `worker_uid`, `region` | Total jobs claimed by this worker |

### Incidents

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `solidping_incidents_active` | Gauge | `organization` | Currently open incidents |
| `solidping_incidents_total` | Counter | `organization`, `check_type` | Total incidents created |

### Process (provided automatically by `prometheus/client_golang`)

The default `promhttp.Handler()` includes Go runtime metrics: `go_goroutines`, `go_memstats_*`, `process_cpu_seconds_total`, `process_open_fds`, etc. These are included at no extra cost.

---

## Cardinality Analysis

| Metric | Max label combinations | Notes |
|--------|----------------------|-------|
| `executions_total` | ~types(25) * statuses(5) * regions(5) * orgs(10) = 6,250 | Acceptable |
| `duration_milliseconds` | Same as above * 13 buckets = ~81K series | Standard for histograms |
| `check_up` | ~checks(500) * regions(5) = 2,500 | Bounded by check count |
| `checks_configured_total` | ~types(25) * orgs(10) * 2 = 500 | Low |
| `workers_active` | ~regions(5) = 5 | Negligible |
| `incidents_active` | ~orgs(10) = 10 | Negligible |

Total estimated series: **<100K** — well within Prometheus comfort zone.

---

## Implementation

### Package Structure

```
server/internal/prommetrics/
├── metrics.go       # Metric definitions and registration
└── middleware.go     # Recording helpers called from check execution
```

### Metric Registration (`metrics.go`)

```go
package prommetrics

import "github.com/prometheus/client_golang/prometheus"

var (
    CheckExecutions = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "solidping_check_executions_total",
            Help: "Total number of check executions",
        },
        []string{"check_type", "status", "region", "organization"},
    )

    CheckDuration = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "solidping_check_duration_milliseconds",
            Help:    "Check execution duration in milliseconds",
            Buckets: []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000},
        },
        []string{"check_type", "status", "region", "organization"},
    )

    SchedulingDelay = prometheus.NewHistogramVec(
        prometheus.HistogramOpts{
            Name:    "solidping_check_scheduling_delay_seconds",
            Help:    "Delay between scheduled and actual execution time",
            Buckets: []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60},
        },
        []string{"region"},
    )

    CheckUp = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "solidping_check_up",
            Help: "1 if check is currently UP, 0 otherwise",
        },
        []string{"check_slug", "check_type", "region", "organization"},
    )

    CheckStatusStreak = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "solidping_check_status_streak",
            Help: "Consecutive results with current status",
        },
        []string{"check_slug", "check_type", "organization"},
    )

    ChecksConfigured = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "solidping_checks_configured_total",
            Help: "Number of configured checks",
        },
        []string{"check_type", "organization", "enabled"},
    )

    WorkersActive = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "solidping_workers_active",
            Help: "Number of active workers",
        },
        []string{"region"},
    )

    WorkerFreeRunners = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "solidping_worker_free_runners",
            Help: "Available runner slots per worker",
        },
        []string{"worker_uid", "region"},
    )

    WorkerJobsClaimed = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "solidping_worker_jobs_claimed_total",
            Help: "Total jobs claimed by worker",
        },
        []string{"worker_uid", "region"},
    )

    IncidentsActive = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "solidping_incidents_active",
            Help: "Currently open incidents",
        },
        []string{"organization"},
    )

    IncidentsTotal = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "solidping_incidents_total",
            Help: "Total incidents created",
        },
        []string{"organization", "check_type"},
    )
)

func Register(reg prometheus.Registerer) {
    reg.MustRegister(
        CheckExecutions, CheckDuration, SchedulingDelay,
        CheckUp, CheckStatusStreak, ChecksConfigured,
        WorkersActive, WorkerFreeRunners, WorkerJobsClaimed,
        IncidentsActive, IncidentsTotal,
    )
}
```

### Recording Helper (`middleware.go`)

```go
func RecordExecution(checkType, status, region, org string, durationMs float64) {
    CheckExecutions.WithLabelValues(checkType, status, region, org).Inc()
    CheckDuration.WithLabelValues(checkType, status, region, org).Observe(durationMs)
}

func RecordSchedulingDelay(region string, delaySeconds float64) {
    SchedulingDelay.WithLabelValues(region).Observe(delaySeconds)
}

func SetCheckStatus(checkSlug, checkType, region, org string, up bool) {
    val := 0.0
    if up {
        val = 1.0
    }
    CheckUp.WithLabelValues(checkSlug, checkType, region, org).Set(val)
}
```

### HTTP Endpoint Registration

In `server/internal/app/server.go`, add the `/metrics` route:

```go
import "github.com/prometheus/client_golang/prometheus/promhttp"

// In setupRoutes() or equivalent:
router.GET("/metrics", bunrouter.HTTPHandler(promhttp.Handler()))
```

The `/metrics` endpoint requires no authentication — it's standard for Prometheus targets and contains no sensitive data (no check configs, no credentials, only aggregate counters and gauges).

### Integration Points

1. **Check execution** (`checkworker/worker.go` → `executeJob`): Call `RecordExecution()` and `RecordSchedulingDelay()` alongside the existing OTel `RecordExecution()` call.

2. **Check status update** (`checkworker/worker.go` → status tracking): Call `SetCheckStatus()` and update `CheckStatusStreak` when status changes.

3. **Check CRUD** (`services/check_service.go`): Update `ChecksConfigured` gauge on create/update/delete.

4. **Worker heartbeat** (`checkworker/worker.go` → heartbeat loop): Update `WorkerFreeRunners` gauge. A periodic goroutine queries active workers to update `WorkersActive`.

5. **Incident lifecycle** (`services/incident_service.go`): Increment `IncidentsTotal` on creation, update `IncidentsActive` on open/close.

---

## Configuration

```yaml
# solidping.yaml
prometheus:
  enabled: true          # default: true
  path: "/metrics"       # default: /metrics
```

Environment variables:
- `SP_PROMETHEUS_ENABLED=true` (default: `true`)
- `SP_PROMETHEUS_PATH=/metrics` (default: `/metrics`)

When disabled, the `/metrics` route is not registered and no Prometheus collectors are initialized (zero overhead).

---

## Example Prometheus Scrape Config

```yaml
scrape_configs:
  - job_name: solidping
    scrape_interval: 15s
    static_configs:
      - targets: ['solidping:4000']
```

---

## Example Alerting Rules

```yaml
groups:
  - name: solidping
    rules:
      - alert: CheckDown
        expr: solidping_check_up == 0
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Check {{ $labels.check_slug }} is DOWN"

      - alert: HighSchedulingDelay
        expr: histogram_quantile(0.95, rate(solidping_check_scheduling_delay_seconds_bucket[5m])) > 10
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Check scheduling delay p95 > 10s in region {{ $labels.region }}"

      - alert: WorkerPoolExhausted
        expr: solidping_worker_free_runners == 0
        for: 2m
        labels:
          severity: critical
        annotations:
          summary: "Worker {{ $labels.worker_uid }} has no free runners"

      - alert: HighFailureRate
        expr: |
          rate(solidping_check_executions_total{status=~"down|timeout|error"}[5m])
          / rate(solidping_check_executions_total[5m]) > 0.1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Check failure rate > 10% for type {{ $labels.check_type }}"
```

---

## Example Grafana Dashboard Queries

```promql
# Check success rate by type (last hour)
1 - (
  rate(solidping_check_executions_total{status=~"down|timeout|error"}[1h])
  / rate(solidping_check_executions_total[1h])
)

# P95 check duration by type
histogram_quantile(0.95, rate(solidping_check_duration_milliseconds_bucket[5m]))

# Active workers by region
solidping_workers_active

# Scheduling delay trend
histogram_quantile(0.95, rate(solidping_check_scheduling_delay_seconds_bucket[5m]))

# Checks currently down
count(solidping_check_up == 0)
```

---

## Testing

- Unit test: Register metrics, call recording helpers, verify metric values via `prometheus/client_golang/testutil`
- Integration test: Start server, scrape `/metrics`, verify expected metric names are present
- Cardinality test: Create N checks, verify series count stays within expected bounds
