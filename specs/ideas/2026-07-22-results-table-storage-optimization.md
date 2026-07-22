# Results table storage optimization

Question: how can we save disk space on the `results` table — possibly by
introducing Postgres extensions?

Analysis date: 2026-07-22 (Claude, requested by Florent).

---

## How it works today

A single hybrid `results` table (`server/internal/db/postgres/migrations/001_v0_1_0.up.sql:431`)
stores two very different things, discriminated by `period_type`:

- **Raw rows** (`period_type='raw'`): one per check execution per region,
  carrying `worker_uid`, `status`, `duration`, and two schemaless JSON blobs
  (`metrics`, `output`). Retained ~24 h (default), then rolled up and
  **deleted** by `server/internal/jobs/jobtypes/job_aggregation.go`.
- **Rollup rows** (`hour`/`day`/`month`): `total_checks`, `successful_checks`,
  `availability_pct`, `duration_min/max/p95/avg`. Hour rows kept 7 d, day rows
  2 months, month rows **forever**. `year` exists in the CHECK constraint but
  is never written — there is no `month→year` aggregation stage in Go.

Each raw row lives through **insert → update → delete**: on insert,
`SaveResultWithStatusTracking` runs an UPDATE clearing the predecessor's
`last_for_status` (`server/internal/db/postgres/postgres.go:1844`), and within
~25 h the aggregation job deletes it. In Postgres, *space* is as much about
this dead-tuple churn as about live bytes: the whole raw tier turns over
daily, which is a permanent VACUUM treadmill, and plain DELETE never returns
disk to the OS.

## Where the bytes go (raw HTTP row)

| Component | Postgres | SQLite |
|---|---|---|
| Tuple/record header + null bitmap (21 cols) | ~32 B | ~23 B |
| 4 UUIDs (uid, org, check, worker) | 64 B | **~144 B** (TEXT, 36 B each) |
| 2–3 timestamps | 16 B | **~64 B** (ISO TEXT, measured on a dev DB) |
| `period_type` text `'raw'` | 5 B | 4 B |
| `region` text | ~8 B | ~8 B |
| status, duration, bool, padding | ~15 B | ~6 B |
| `metrics` JSON — for HTTP just `{"duration_ms":X}` | ~35 B | ~20 B |
| `output` JSON — `{url, method, status_code}` | ~80–150 B | ~80–150 B |
| **Heap total** | **~260–320 B** | **~350–420 B** |
| Index entries (PK + raw_idx + last_for_status idx) | ~100 B | ~90 B |

The useful information content of that row is roughly 30 bytes. The rest is
encoding overhead, duplication, or repetition of per-check constants — the
URL and method of a check never change between executions, yet they are
re-stored 1 440×/day per check at the default 1-min period.

## Recommendations, ranked

### Tier 1 — free wins, no restructuring

1. **Delete `last_for_status` + its partial index + the clearing UPDATE.**
   Verified: zero readers — no SELECT anywhere filters on it; the only
   `WHERE last_for_status` is the maintenance UPDATE itself. The dashboard
   uses `DISTINCT ON (check_uid)` instead
   (`server/internal/db/postgres/postgres.go:2040`). Removing it kills one
   UPDATE per insert — one dead tuple + index churn + WAL per result, i.e.
   roughly **halves write-side bloat** — and drops a useless index
   (`idx_results_last_for_status`).
2. **Stop copying the last raw `output` blob into hourly rollups**
   (`job_aggregation.go:974-977`). Hour rows live 7× longer than raw rows, so
   this blob costs 7× more there than in the raw tier. If "last error of the
   hour" is wanted, store a reference or a truncated message. (Day/month
   rollups already empty it.)
3. **Stop writing `metrics={"duration_ms":X}` for HTTP** — it duplicates the
   `duration` column byte-for-byte (`server/internal/checkers/checkhttp/checker.go:385`),
   ~35 B/row of pure waste on the most common check type.
4. **Drop `availability_pct`** — derivable from
   `successful_checks/total_checks` at read time; 8 B per rollup row and one
   less way for the two to disagree.
5. Adjacent (I/O, not disk): `ListResults` selects all columns including both
   JSON blobs even for uptime bars / status pages / badges that only need
   status counts (`postgres.go:1876`, consumers in `uptimebar/`). Add column
   projection.

### Tier 2 — schema v2: split the hybrid table

Split into **`results_raw`** and **`results_rollup`**. That alone removes
`period_type` from every row and index, lets each table drop the other's
NULL columns (8+ per row), gives each its own unique constraints, and enables
per-table lifecycle tricks (partitioning below). While at it:

- **Drop `organization_uid` from `results_raw`** (keep on rollups for cheap
  org-wide SLA queries). Every read path already knows the check UIDs; tenant
  isolation can ride the `checks` join. Saves 16 B/row in heap **and** 16 B
  per index entry, since it leads `results_raw_idx`.
- **`region` → smallint FK** (or NULL for the default region): ~6 B/row and a
  smaller rollup unique index.
- **Pack columns by alignment** (uuids/timestamps first, then 4-byte, 2-byte,
  bools, varlena last) — a few more bytes/row for free.
- **SQLite encoding fix — biggest single-backend win**: store UUIDs as
  16-byte BLOBs and timestamps as INTEGER epoch-ms. Measured on the dev DB:
  UUID text costs 108 B/row and timestamp text ~64 B/row; binary forms cost
  48+16. That's **~110 B/row saved, roughly 30–40 % of a raw row**, on the
  backend most self-hosters run.
  ⚠ Interacts with `specs/ideas/2025-12-28-drop-sqlite.md` — if SQLite
  support is dropped, this bullet disappears and the whole plan simplifies.

### Tier 3 — deduplicate `output` (biggest payload win)

`output` is the dominant variable cost and is massively repetitive: a healthy
check produces the *identical* `{url, method, status_code}` blob thousands of
times. Two designs, in order of preference:

1. **Store output only when it changes** (or only on non-up status): keep a
   content hash per check; write `output` NULL when identical to the previous
   result; readers fall back to "last non-null output". For a check that's
   green all month at 1-min period: ~150 B × 43 200 rows ≈ 6 MB/check/month
   avoided — typically **50–80 % of total raw-tier bytes**.
2. Or a content-addressed side table `result_outputs(hash pk, body)` with an
   8-byte reference — more general, but adds a join and GC complexity. Start
   with option 1.

Regardless of design: the `js`/`browser` checkers pass through **unbounded
user-controlled maps** into `metrics`/`output`
(`server/internal/checkers/checkjs/checker.go:318`) — add a size cap.

### Extensions — verdict

- **Native declarative partitioning (no extension) on `results_raw`, daily
  partitions — do this one.** Since every raw row is deleted within ~25 h,
  steady state is "the whole table turns over daily". With daily partitions,
  the aggregation job stops DELETE-ing rolled-up rows and retention becomes
  `DROP TABLE partition`: instant, zero dead tuples, zero delete-WAL, and
  disk actually returned to the OS. **pg_partman** merely automates partition
  creation/dropping; a small scheduled job in the existing jobs framework
  does the same without a new dependency.
- **TimescaleDB — not now.** What it offers is real (columnar compression
  typically >90 %, continuous aggregates that would replace most of
  `job_aggregation.go`, `drop_chunks` retention). But raw retention is only
  24 h so the live raw set is small (~60 MB per 100 one-minute checks — churn
  is the problem, and partitioning fixes that dependency-free); dual-backend
  support means the hand-rolled aggregation must stay anyway; and it's
  deployment friction for self-hosters on vanilla Postgres. **The moment this
  flips:** selling longer raw retention (e.g. 90 d of per-request detail — a
  genuine differentiator, most competitors cap history aggressively) makes
  columnar compression the enabling tech; revisit as an *optional* "use if
  installed" path then.
- **BRIN**: not applicable — hot queries are per-check, which needs the btree.
- **Per-column lz4/TOAST compression won't help**: these JSON values are far
  below the ~2 KB TOAST threshold. Key repetition is structural; only
  Tiers 1–3 fix it.

### Correctness-adjacent finding

`month` rows are never pruned and there is no `year` stage despite the CHECK
constraint allowing it. Size-wise negligible (12 rows/check/region/year), but
either implement `month→year` or drop `'year'` from the constraint — and
decide explicitly that month rows are forever (arguably a nice "lifetime SLA
history" property).

## Expected impact

For the canonical workload (HTTP checks, 1-min period):

- Tier 1 alone cuts write amplification roughly in half and trims ~40 B/row.
- Tier 1+2 brings a Postgres raw row from ~360 B (heap+indexes) to ~200 B,
  and a SQLite row down ~45 %.
- Tier 3 removes another 50–80 % of raw-tier payload bytes for healthy checks.
- Partitioning eliminates delete/vacuum churn entirely and makes retention
  instant.

Combined: expect **3–5× less disk** for the results workload without any
extension dependency, with TimescaleDB held in reserve as the unlock for a
longer-retention paid tier.
