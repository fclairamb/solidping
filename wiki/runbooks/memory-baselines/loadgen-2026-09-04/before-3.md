# SolidPing loadgen report

- Backend: `sqlite-before`
- Created checks: 200 (period 10s)
- Target latency: 10ms
- Elapsed: 1m30s
- Generated: 2026-09-04T10:24:07+02:00

## Headline

| Metric | Value |
|---|---|
| Total check executions | 1804 |
| Achieved checks/min | **1202.7** |
| Rate-limited executions | 0 |
| DB busy retries (delta) | 0 |

## Check stage timings (samples observed during the run)

| Stage | Count | p50 | p95 | p99 |
|---|---:|---:|---:|---:|
| fetch | 1466 | 3.0ms | 4.9ms | 8.0ms |
| execute | 1804 | 30.0ms | 48.0ms | 49.6ms |
| save_result | 1813 | 2.6ms | 5.1ms | 9.7ms |
| process_incident | 1814 | 1.5ms | 7.8ms | 30.9ms |
| release_lease | 1814 | 899µs | 4.8ms | 8.9ms |

## DB query timings

| Backend | Count | p50 | p95 | p99 |
|---|---:|---:|---:|---:|
| sqlite | 19881 | 403µs | 4.1ms | 6.2ms |

## DB pool snapshot (final)

| Backend | Max open | Open | In use | Idle | Wait count (delta) | Wait duration (delta) |
|---|---:|---:|---:|---:|---:|---:|
| sqlite | 1 | 1 | 0 | 1 | 4309 | 5.71s |

## HTTP request timings (selected routes)

| Route | Count | p50 | p95 | p99 |
|---|---:|---:|---:|---:|

## Claim-jobs outcomes

| Outcome | Count |
|---|---:|
| jobs | 1466 |
| empty | 0 |
| lock_conflict | 0 |
| error | 0 |

## Notes

- All p50/p95/p99 figures are estimated by linear interpolation across Prometheus histogram buckets;
  for finer-grained percentiles run PromQL `histogram_quantile()` against the live /metrics endpoint.
- Counter values are deltas measured between baseline and final scrapes.
- DB pool gauges are final-snapshot values; a non-zero `Wait duration` here is the strongest signal that
  the pool is the bottleneck.
