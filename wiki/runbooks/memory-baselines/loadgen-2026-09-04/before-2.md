# SolidPing loadgen report

- Backend: `sqlite-before`
- Created checks: 200 (period 10s)
- Target latency: 10ms
- Elapsed: 1m30s
- Generated: 2026-09-04T10:21:04+02:00

## Headline

| Metric | Value |
|---|---|
| Total check executions | 1987 |
| Achieved checks/min | **1324.7** |
| Rate-limited executions | 0 |
| DB busy retries (delta) | 0 |

## Check stage timings (samples observed during the run)

| Stage | Count | p50 | p95 | p99 |
|---|---:|---:|---:|---:|
| fetch | 1367 | 3.0ms | 4.9ms | 8.4ms |
| execute | 1987 | 30.0ms | 48.1ms | 49.7ms |
| save_result | 1987 | 2.6ms | 7.0ms | 15.4ms |
| process_incident | 1987 | 2.5ms | 9.2ms | 38.1ms |
| release_lease | 1987 | 1.5ms | 4.9ms | 9.2ms |

## DB query timings

| Backend | Count | p50 | p95 | p99 |
|---|---:|---:|---:|---:|
| sqlite | 20552 | 419µs | 4.5ms | 8.3ms |

## DB pool snapshot (final)

| Backend | Max open | Open | In use | Idle | Wait count (delta) | Wait duration (delta) |
|---|---:|---:|---:|---:|---:|---:|
| sqlite | 1 | 1 | 0 | 1 | 7473 | 10.69s |

## HTTP request timings (selected routes)

| Route | Count | p50 | p95 | p99 |
|---|---:|---:|---:|---:|

## Claim-jobs outcomes

| Outcome | Count |
|---|---:|
| jobs | 1367 |
| empty | 0 |
| lock_conflict | 0 |
| error | 0 |

## Notes

- All p50/p95/p99 figures are estimated by linear interpolation across Prometheus histogram buckets;
  for finer-grained percentiles run PromQL `histogram_quantile()` against the live /metrics endpoint.
- Counter values are deltas measured between baseline and final scrapes.
- DB pool gauges are final-snapshot values; a non-zero `Wait duration` here is the strongest signal that
  the pool is the bottleneck.
