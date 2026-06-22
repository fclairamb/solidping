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
