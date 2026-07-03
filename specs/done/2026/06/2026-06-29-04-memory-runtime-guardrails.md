# Memory runtime guardrails

**Status:** done
**Date:** 2026-06-29
**Follows:** `2026-06-28-05-memory-consumption-analysis.md` (measured & instrumented;
deferred the actual fixes to "separate remediation specs"). This is one of them.

## Goal

Apply the *safe, low-risk* memory guardrails identified by the consumption
analysis — bound peak/steady-state memory without changing business logic or
weakening security. Out of scope: the JSON-column (`JSONMap.Scan`) decode-path
refactor, which is the largest churn source (~22% of cumulative allocations) but
needs its own spec.

## Measurement that motivated this

pprof on the running dev server (`localhost:6060`):

- **Idle is already lean**: ~13 MB live heap, ~82 MB RSS, 38 goroutines (bounded
  worker pool — no goroutine-per-check).
- **Churn (cumulative `alloc_space`)** dominated by DB row scanning and auth:
  `JSONMap.Scan`→`json.Unmarshal` 264 MB (22%), `bun.MakeQueryBytes` 145 MB
  (12%), `argon2.initBlocks` 128 MB (11%), `sqlite.columnText` 92 MB (8%).
- **Gaps**: no `GOMEMLIMIT`/`GOGC`/`SetMemoryLimit` anywhere; Postgres pool
  unbounded (`database/sql` default = unlimited); argon2 peak = 64 MB ×
  concurrent logins.

## What shipped

1. **Runtime GC levers** — new `internal/memlimit` package applied at startup in
   `main.go`. Sets `GOMEMLIMIT` (soft cap) and optionally `GOGC`.
   - Precedence: native `GOMEMLIMIT`/`GOGC` env wins → else `SP_RUNTIME_*` config
     → else cgroup auto-derivation → else runtime default.
   - **cgroup-aware auto** (`SP_RUNTIME_AUTO_MEMORY_LIMIT`, default on): on a
     container with a memory limit, sets the cap to `limit ×
     SP_RUNTIME_MEMORY_LIMIT_RATIO` (default 0.9). No-op off-container, so
     Kubernetes pods get an OOM guardrail with zero operator action.
   - Knobs: `SP_RUNTIME_MEMORY_LIMIT` (human size/bytes),
     `SP_RUNTIME_AUTO_MEMORY_LIMIT`, `SP_RUNTIME_MEMORY_LIMIT_RATIO`,
     `SP_RUNTIME_GC_PERCENT`. Default config is a behavioral no-op off-container.
   - Effective values logged at startup and exposed in `GET /api/mgmt/memory`
     (`runtime.goMemLimitBytes`, `runtime.goMaxProcs`).
2. **Bounded Postgres pool** — `DatabaseConfig.{MaxOpenConns,MaxIdleConns,
   ConnMaxLifetime}` (defaults 25 / 10 / 1h; `SP_DB_MAX_OPEN_CONNS` etc.) applied
   in `postgres.New`. SQLite already pins a single writer.
3. **Bounded argon2 concurrency** — a counting semaphore caps concurrent
   argon2id derivations at `min(GOMAXPROCS, 4)`, so a login burst peaks at
   ~`cap × 64 MB` instead of `N × 64 MB`. Memory params unchanged (security
   preserved); only concurrency is bounded.

## Verification

- Unit tests: `internal/memlimit` (decision logic + `apply` smoke asserting the
  limit lands in the runtime), config env readers, argon2 concurrency.
- End-to-end: side-car server confirmed `source=config` for `SP_RUNTIME_*` and
  native-env precedence (`GOMEMLIMIT` wins).
- Before/after under identical 80-login load (scraped `/metrics`):

  | Config | heap_inuse | RSS | GC cycles |
  |--------|-----------:|----:|----------:|
  | default | 138.8 MB | 255.6 MB | 45 |
  | `GOGC=25` | 73.9 MB | 190.9 MB | 87 |

  ≈ −47% heap / −25% RSS for ~2× GC — the expected CPU-for-memory trade.

## Follow-ups (not in this spec)

- `JSONMap.Scan` decode path (lazy/column-pruned) — top churn source.
- `bun` query-bytes churn (batch per-result inserts).
- DEK cache / event-listener growth bounds (analysis-spec suspects #2, #7).
