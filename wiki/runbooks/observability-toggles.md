# Observability toggles

Three independent env vars control which observability surfaces are active:

| Env var | Default | Effect |
|---|---|---|
| `SP_PROMETHEUS_ENABLED` | `true` | Master switch for the `/metrics` HTTP handler. When `false`, the endpoint returns 404 regardless of the token below. Metric collection itself stays on — only the scrape endpoint is gated. |
| `SP_METRICS_SCRAPE_TOKEN` | - | Bearer token required to scrape `/metrics` (spec 2026-09-25-25). Unset → 404, indistinguishable from the endpoint not existing; set → every request needs `Authorization: Bearer <token>` or gets 401. Also settable as the `metrics.scrape_token` system parameter (secret, DB-stored), overlaid onto config at boot — env wins over DB. |
| `SP_PROFILER_ENABLED` | `false` | Starts the pprof HTTP server. Listen address controlled by `SP_PROFILER_LISTEN` (default `localhost:6060`). |
| `SP_OTEL_ENABLED` | `false` | Enables OpenTelemetry span export. HTTP and DB instrumentation record spans only when this is `true`. |

The three original toggles are independent — enabling one does not enable any other. `SP_METRICS_SCRAPE_TOKEN` only matters when `SP_PROMETHEUS_ENABLED` is true.
