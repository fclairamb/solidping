# OpenTelemetry Support

## Goal

Add optional OpenTelemetry support for the three pillars: **logging**, **tracing**, and **metrics**. When enabled, SolidPing exports telemetry to an OTLP-compatible collector (e.g., Grafana Alloy, Jaeger, Prometheus). When disabled, there is zero overhead — all providers are noop.

The primary use case is **reporting check execution status** to external observability platforms, enabling dashboards, alerts, and correlation with other infrastructure telemetry.

## Configuration

All settings use the `SP_OTEL_*` prefix and are disabled by default.

| Environment Variable | Default | Description |
|---|---|---|
| `SP_OTEL_ENABLED` | `false` | Master switch — nothing initializes when false |
| `SP_OTEL_ENDPOINT` | `localhost:4317` | OTLP collector endpoint |
| `SP_OTEL_PROTOCOL` | `grpc` | Transport protocol: `grpc` or `http` |
| `SP_OTEL_INSECURE` | `false` | Skip TLS verification (development) |
| `SP_OTEL_LOGS` | `false` | Forward slog logs to OTel collector |
| `SP_OTEL_TRACES` | `false` | Enable distributed tracing |
| `SP_OTEL_METRICS` | `false` | Enable metrics export |

Config struct added to `back/internal/config/config.go`:

```go
type OTelConfig struct {
    Enabled  bool   `koanf:"enabled"`
    Endpoint string `koanf:"endpoint"`
    Protocol string `koanf:"protocol"`
    Insecure bool   `koanf:"insecure"`
    Logs     bool   `koanf:"logs"`
    Traces   bool   `koanf:"traces"`
    Metrics  bool   `koanf:"metrics"`
}
```

## New Package: `back/internal/otelsetup/`

A `Provider` struct with `Start(ctx)` and `Shutdown(ctx)` methods, following the same lifecycle pattern as `internal/profiler/`.

- Creates OTLP exporters (gRPC or HTTP based on `Protocol`)
- Creates an OTel resource with `service.name=solidping`, `service.version`
- Initializes TracerProvider, MeterProvider, and/or LoggerProvider based on config flags
- Sets global providers via `otel.SetTracerProvider()`, `otel.SetMeterProvider()`
- When `Enabled=false`, `Start()` is a no-op

## Pillar 1: Logging

**Goal**: Forward structured logs to OTel collector alongside existing stdout output.

**Approach**:
- Use the `otelslog` bridge (`go.opentelemetry.io/contrib/bridges/otelslog`) to create an `slog.Handler` backed by the OTel LoggerProvider
- Write a simple `FanoutHandler` (~30 lines, in `back/internal/utils/slog/fanout.go`) that forwards log records to multiple handlers
- When OTel logs are enabled, logs go to both the existing text handler (stdout) and the OTel handler

**Integration point**: `main.go` `setupLogger()` — reconfigure after OTel provider starts.

## Pillar 2: Tracing

**Goal**: Trace check execution lifecycle and API requests.

### Check Execution Traces

**Integration point**: `back/internal/checkworker/worker.go`, `executeJob()` method.

A span wraps the full check execution lifecycle:

```go
ctx, span := otel.Tracer("solidping.check").Start(ctx, "check.execute",
    trace.WithAttributes(
        attribute.String("check.uid", checkJob.CheckUID),
        attribute.String("check.slug", checkJob.CheckSlug),
        attribute.String("check.name", checkJob.CheckName),
        attribute.String("check.type", checkJob.Type),
        attribute.String("check.region", region),
        attribute.String("organization.uid", checkJob.OrganizationUID),
    ),
)
defer span.End()
```

- Child spans for DB save (`saveResult`) and incident processing
- `span.SetStatus(codes.Error, ...)` on check failure/timeout
- `span.RecordError(err)` for execution errors

When tracing is disabled, `otel.Tracer()` returns a noop tracer — zero overhead.

### API Request Traces

OTel middleware on bunrouter for incoming HTTP requests:
- Span per request with attributes: method, path, status code, duration
- Uses `otel.Tracer("solidping.check")`

## Pillar 3: Metrics

**Goal**: Export check execution metrics for dashboards and alerting.

### Instruments

| Metric | Type | Unit | Description |
|---|---|---|---|
| `check.duration` | Float64Histogram | ms | Check execution duration |
| `check.executions.total` | Int64Counter | 1 | Total check executions |
| `check.executions.failed` | Int64Counter | 1 | Failed check executions |
| `check.status` | Int64UpDownCounter | 1 | Current status per check (1=up, 0=down) |
| `check.schedule_delay` | Float64Histogram | ms | Delay between scheduled and actual execution |
| `worker.runners.active` | Int64UpDownCounter | 1 | Currently active runner goroutines |

### Attributes

Every check metric carries these attributes:
- `check.uid` — Unique check identifier
- `check.slug` — URL-friendly check identifier (enables human-readable dashboards)
- `check.name` — Human-readable check name
- `check.type` — Protocol type (http, tcp, dns, etc.)
- `region` — Worker region
- `organization.uid` — Organization scope
- `status` — Result status string (up, down, timeout, error)

### Integration

A `CheckMetrics` struct in `back/internal/otelsetup/metrics.go` wraps the OTel instruments and provides a `RecordExecution()` method. Called in `checkworker/worker.go` after `r.stats.AddMetric()` (line 448).

All metrics and traces use the `solidping.check` instrumentation scope:

```go
otel.Meter("solidping.check")
otel.Tracer("solidping.check")
```

When metrics are disabled, `otel.Meter()` returns a noop meter — zero overhead.

## Application Lifecycle

```
config.Load()
  ↓
otelsetup.New(&cfg.OTel).Start(ctx)       ← init providers
  ↓
setupLogger(cfg.LogLevel, loggerProvider)  ← bridge slog if OTel logs enabled
  ↓
app.NewServer(ctx, cfg)
  ↓
server.Start(ctx)                          ← check workers use global tracer/meter
  ↓
server.Close()
  ↓
otelProvider.Shutdown(ctx)                 ← flush all telemetry before exit
```

OTel starts before the server and shuts down after it, ensuring all telemetry (including shutdown logs) is captured and flushed.

## Dependencies

```
go.opentelemetry.io/otel/sdk
go.opentelemetry.io/otel/sdk/metric
go.opentelemetry.io/otel/sdk/log
go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc
go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc
go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc
go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp
go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp
go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp
go.opentelemetry.io/contrib/bridges/otelslog
```

Note: `go.opentelemetry.io/otel` and `go.opentelemetry.io/otel/trace` are already indirect dependencies (from Bun ORM).

## Files to Create/Modify

| File | Action |
|---|---|
| `back/internal/config/config.go` | Add `OTelConfig` struct and field |
| `back/internal/otelsetup/otelsetup.go` | New — Provider lifecycle |
| `back/internal/otelsetup/metrics.go` | New — CheckMetrics instruments |
| `back/internal/utils/slog/fanout.go` | New — FanoutHandler for multi-output logging |
| `back/main.go` | Wire OTel init/shutdown, modify setupLogger |
| `back/internal/checkworker/worker.go` | Add tracing spans and metrics recording |
| `back/internal/app/server.go` | Add OTel middleware to bunrouter |

## Testing

- Unit tests for `otelsetup` package: verify disabled config is noop, enabled config creates providers
- Integration: run with `SP_OTEL_ENABLED=true SP_OTEL_ENDPOINT=localhost:4317` against a local collector (e.g., `docker run otel/opentelemetry-collector`)
- Verify metrics appear in collector output
- Verify traces have correct attributes
- Verify logs are forwarded while still appearing on stdout
