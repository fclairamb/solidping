# SolidPing loadgen report

- Backend: `sqlite-after`
- Created checks: 200 (period 10s)
- Target latency: 10ms
- Elapsed: 1m30s
- Generated: 2026-09-04T10:25:39+02:00

## Headline

| Metric | Value |
|---|---|
| Total check executions | 1989 |
| Achieved checks/min | **1326.0** |
| Rate-limited executions | 0 |
| DB busy retries (delta) | 0 |

## Check stage timings (samples observed during the run)

| Stage | Count | p50 | p95 | p99 |
|---|---:|---:|---:|---:|
| fetch | 1422 | 3.0ms | 4.9ms | 7.7ms |
| execute | 1989 | 30.1ms | 48.1ms | 49.7ms |
| save_result | 1989 | 2.2ms | 5.0ms | 9.6ms |
| process_incident | 1989 | 2.1ms | 7.6ms | 24.3ms |
| release_lease | 1989 | 1.3ms | 4.8ms | 8.9ms |

## DB query timings

| Backend | Count | p50 | p95 | p99 |
|---|---:|---:|---:|---:|
| sqlite | 20891 | 402µs | 4.2ms | 6.9ms |

## DB pool snapshot (final)

| Backend | Max open | Open | In use | Idle | Wait count (delta) | Wait duration (delta) |
|---|---:|---:|---:|---:|---:|---:|
| sqlite | 1 | 1 | 0 | 1 | 6958 | 8.20s |

## HTTP request timings (selected routes)

| Route | Count | p50 | p95 | p99 |
|---|---:|---:|---:|---:|

## Claim-jobs outcomes

| Outcome | Count |
|---|---:|
| jobs | 1422 |
| empty | 0 |
| lock_conflict | 0 |
| error | 0 |

## Notes

- All p50/p95/p99 figures are estimated by linear interpolation across Prometheus histogram buckets;
  for finer-grained percentiles run PromQL `histogram_quantile()` against the live /metrics endpoint.
- Counter values are deltas measured between baseline and final scrapes.
- DB pool gauges are final-snapshot values; a non-zero `Wait duration` here is the strongest signal that
  the pool is the bottleneck.
