# SolidPing loadgen report

- Backend: `sqlite-after`
- Created checks: 200 (period 10s)
- Target latency: 10ms
- Elapsed: 1m30s
- Generated: 2026-09-04T10:19:09+02:00

## Headline

| Metric | Value |
|---|---|
| Total check executions | 1898 |
| Achieved checks/min | **1265.3** |
| Rate-limited executions | 0 |
| DB busy retries (delta) | 0 |

## Check stage timings (samples observed during the run)

| Stage | Count | p50 | p95 | p99 |
|---|---:|---:|---:|---:|
| fetch | 1665 | 2.9ms | 4.8ms | 6.7ms |
| execute | 1898 | 30.0ms | 48.0ms | 49.6ms |
| save_result | 1899 | 2.4ms | 4.8ms | 9.0ms |
| process_incident | 1899 | 1.2ms | 4.8ms | 9.4ms |
| release_lease | 1899 | 840µs | 4.6ms | 5.0ms |

## DB query timings

| Backend | Count | p50 | p95 | p99 |
|---|---:|---:|---:|---:|
| sqlite | 21695 | 388µs | 3.7ms | 4.8ms |

## DB pool snapshot (final)

| Backend | Max open | Open | In use | Idle | Wait count (delta) | Wait duration (delta) |
|---|---:|---:|---:|---:|---:|---:|
| sqlite | 1 | 1 | 0 | 1 | 3291 | 3.16s |

## HTTP request timings (selected routes)

| Route | Count | p50 | p95 | p99 |
|---|---:|---:|---:|---:|

## Claim-jobs outcomes

| Outcome | Count |
|---|---:|
| jobs | 1665 |
| empty | 0 |
| lock_conflict | 0 |
| error | 0 |

## Notes

- All p50/p95/p99 figures are estimated by linear interpolation across Prometheus histogram buckets;
  for finer-grained percentiles run PromQL `histogram_quantile()` against the live /metrics endpoint.
- Counter values are deltas measured between baseline and final scrapes.
- DB pool gauges are final-snapshot values; a non-zero `Wait duration` here is the strongest signal that
  the pool is the bottleneck.
