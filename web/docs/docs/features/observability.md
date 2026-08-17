---
sidebar_position: 4
title: Observability
---

# Observability

SolidPing integrates with popular observability tools to help you monitor the monitoring platform itself. All three integrations are configured independently and can be enabled side by side.

## Prometheus Metrics

SolidPing exposes a Prometheus-compatible metrics endpoint (enabled by default).

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_PROMETHEUS_ENABLED` | `true` | Enable the metrics endpoint |
| `SP_PROMETHEUS_PATH` | `/metrics` | Path of the metrics endpoint |

### Scrape Configuration

```yaml
scrape_configs:
  - job_name: solidping
    static_configs:
      - targets: ['solidping:4000']
    metrics_path: /metrics
```

### Available Metrics

Metrics cover check execution, worker health, job scheduling, and application performance.

#### Database query metrics

`solidping_db_query_duration_seconds` observes SQL query latency, labeled by `operation` (`SELECT`, `INSERT`, ...), `backend` (`postgres`/`sqlite`), `status` (`ok`/`error`) and `callsite` — the code path that issued the query (e.g. `uptimebar.bucket_availability`, `results.list`), or `unlabelled` when the calling package hasn't been annotated. Only a small, fixed set of hot read paths currently set `callsite`, so the label's value set stays bounded; it will never contain raw SQL text or request-specific data.

**If you built dashboards or alerts against an earlier version:** `callsite` widens this metric's label set, so a selector that used to return one series per `operation`/`backend`/`status` combination now returns one series *per combination per distinct `callsite` value* — a panel that plotted a single line can now plot several, and an alert written against a single series now evaluates once per callsite. The concrete breakages this can cause: binary operations and `on()`/`ignoring()` matching that assumed identical label sets between two vectors (these can now fail with "many-to-many matching not allowed" or silently drop samples); recording rules and alerts that assumed one series per `operation`/`backend`; and a `histogram_quantile` over a `sum by (...)` that doesn't include every distinguishing label. If you want the pre-upgrade shape back, aggregate the new label away explicitly rather than adding a wildcard matcher — e.g. `sum by (operation, backend, status, le) (rate(solidping_db_query_duration_seconds_bucket[5m]))`.

Queries slower than `db.slow_query_threshold` (default `500ms`, `SP_DB_SLOW_QUERY_THRESHOLD`; see [Database configuration](../configuration/database.md)) are also logged at `WARN` as `slow SQL query`, throttled to at most one line per normalized statement per minute (further occurrences are counted in a `suppressed` field on the next line). The logged statement always has literal values stripped, regardless of the `Verbose` SQL-logging setting.

`solidping_results_rows` gauges the total row count in the `results` table, labeled by `period_type` (`raw`, `hour`, `day`, `month`). It's refreshed on the background aggregation job's own cadence (at most once every 5 minutes, not per-request), so it's cheap to scrape but may lag a live row count by a few minutes — useful as an early warning that ingest is outgrowing Postgres `shared_buffers` well before a table scan turns disk-bound.

### Kubernetes ServiceMonitor

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: solidping
spec:
  selector:
    matchLabels:
      app: solidping
  endpoints:
    - port: http
      path: /metrics
      interval: 30s
```

## Sentry

SolidPing supports Sentry for error tracking and performance monitoring. Set a DSN to enable it.

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_SENTRY_DSN` | - | Sentry DSN (empty = disabled) |
| `SP_SENTRY_ENVIRONMENT` | - | Environment name (`development`, `staging`, `production`) |

```bash
SP_SENTRY_DSN=https://your-key@sentry.io/your-project
SP_SENTRY_ENVIRONMENT=production
```

Additional tuning such as the traces sample rate and debug logging is available through the `sentry` section of `config.yml`:

```yaml
sentry:
  dsn: https://your-key@sentry.io/your-project
  environment: production
  traces_sample_rate: 0.1   # 0.0 to 1.0
  debug: false
```

## OpenTelemetry

SolidPing supports OpenTelemetry for distributed tracing, metrics, and log export over OTLP. Traces, metrics, and logs are toggled independently.

| Variable | Default | Description |
|----------|---------|-------------|
| `SP_OTEL_ENABLED` | `false` | Enable OpenTelemetry export |
| `SP_OTEL_ENDPOINT` | - | OTLP collector endpoint |
| `SP_OTEL_PROTOCOL` | - | OTLP protocol: `http` or `grpc` |
| `SP_OTEL_INSECURE` | `false` | Skip TLS verification for the collector |
| `SP_OTEL_TRACES` | - | Export traces |
| `SP_OTEL_METRICS` | - | Export metrics |
| `SP_OTEL_LOGS` | - | Export logs |

```yaml
otel:
  enabled: true
  endpoint: otel-collector:4317
  protocol: grpc
  traces: true
  metrics: true
  logs: false
```
