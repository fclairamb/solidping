# Committed memory baselines

`make bench-memory` writes its JSON report to `bench-results/`, which is
gitignored — so the numbers in the runbook would otherwise trace only to prose,
and the next person measuring a change would have to re-derive a baseline before
they could compare against one.

The runs kept here are the ones the runbook quotes. They exist to be fed back
into the harness:

```bash
make bench-memory BENCH_MEM_MODE=docker BENCH_MEM_LABEL=candidate \
     BENCH_MEM_COMPARE=wiki/runbooks/memory-baselines/2026-09-04-docker-arm64.json
```

`--compare` will flag any delta smaller than the baseline's own inter-run spread
as *not significant*, which is the whole point of keeping the file rather than a
table.

| file | what it is |
|---|---|
| `2026-09-04-docker-arm64.json` | The state of `batch/2026-09-03` after spec 2026-09-04-01. Docker mode, linux/**arm64** image built from that tree by `make bench-memory-image`, `--memory=1g --cpus=1`, 45 s warm-up / 60 s window / 3 repetitions, on a 10-CPU Docker Desktop VM. Scenarios: `idle-all-sqlite`, `checks-500`, `docs-crawl`. |

**Read the caveats in the file itself** — every report carries its mode, host,
image, protocol and any `-env` overrides, precisely so a number can never be
quoted without them. In particular this is **not** the production linux/amd64
image, and the 45 s warm-up is shorter than the harness default of 5 min, so the
`idle` row is a *warm* reading that still holds the startup heap (see
`../memory-profiling.md` §5).

A new baseline belongs here when the numbers it replaces have stopped being
true — not on every run. Keep the old file alongside the new one; a baseline
that gets overwritten cannot be diffed against.
