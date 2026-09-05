# SolidPing loadgen report

- Backend: `sqlite-after`
- Created checks: 200 (period 10s)
- Target latency: 10ms
- Elapsed: 1m30s
- Generated: 2026-09-04T10:22:35+02:00

## Headline

| Metric | Value |
|---|---|
| Total check executions | 1978 |
| Achieved checks/min | **1318.7** |
| Rate-limited executions | 0 |
| DB busy retries (delta) | 0 |

## Check stage timings (samples observed during the run)

| Stage | Count | p50 | p95 | p99 |
|---|---:|---:|---:|---:|
| fetch | 1491 | 2.9ms | 4.9ms | 8.0ms |
| execute | 1978 | 30.0ms | 48.0ms | 49.6ms |
| save_result | 1978 | 1.6ms | 6.1ms | 12.3ms |
| process_incident | 1978 | 1.4ms | 8.3ms | 33.5ms |
| release_lease | 1978 | 852µs | 4.9ms | 9.4ms |

## DB query timings

| Backend | Count | p50 | p95 | p99 |
|---|---:|---:|---:|---:|
| sqlite | 21240 | 379µs | 4.2ms | 7.9ms |

## DB pool snapshot (final)

| Backend | Max open | Open | In use | Idle | Wait count (delta) | Wait duration (delta) |
|---|---:|---:|---:|---:|---:|---:|
| sqlite | 1 | 1 | 0 | 1 | 6392 | 8.42s |

## HTTP request timings (selected routes)

| Route | Count | p50 | p95 | p99 |
|---|---:|---:|---:|---:|

## Claim-jobs outcomes

| Outcome | Count |
|---|---:|
| jobs | 1491 |
| empty | 0 |
| lock_conflict | 0 |
| error | 0 |

## Notes

- All p50/p95/p99 figures are estimated by linear interpolation across Prometheus histogram buckets;
  for finer-grained percentiles run PromQL `histogram_quantile()` against the live /metrics endpoint.
- Counter values are deltas measured between baseline and final scrapes.
- DB pool gauges are final-snapshot values; a non-zero `Wait duration` here is the strongest signal that
  the pool is the bottleneck.
