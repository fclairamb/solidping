# Vortex on S3 for compacted check results

Push the rolled-up `hour` / `day` / `month` rows out of PostgreSQL into a
columnar object store layout (Vortex on S3), keeping only "hot" raw and
recent rollups in the relational DB.

This document is an **idea exploration with an honest verdict**, not a
ready-to-implement plan. Verdict up front: the *direction* is sound, and
with `LaurieRhodes/vortex-go` providing a Go-native reader/writer that
plugs into any `io.Reader` / `io.Writer` (and therefore directly into
the AWS SDK's S3 streaming API), the path is much more credible than I
first assumed. The remaining hesitations are about library maturity and
format stability, not about language ecosystem. Read on.

---

## 1. Where the data lives today

`results` is a single Postgres/SQLite table that holds four very different
shapes of data, distinguished by `period_type`:

| period_type | retention default | row content |
|-------------|-------------------|-------------|
| `raw`       | 1 hour            | per-execution: status, duration, output (jsonb), metrics (jsonb), worker, region |
| `hour`      | 1 day             | per check×region×hour: total/successful checks, availability_pct, duration min/max/p95, aggregated metrics |
| `day`       | 1 month           | per check×region×day: same shape as hour |
| `month`     | unbounded         | per check×region×month: same shape as hour |

The aggregation job (`server/internal/jobs/jobtypes/job_aggregation.go`)
walks raw → hour → day → month, **deletes** the source rows after each
rollup, and runs hourly per organization.

Key observations for any storage redesign:

1. **Aggregated rows are tiny but numerous and immutable.** Once
   `period_end < now()` and the rollup has run, the row never changes.
   It's a perfect cold-storage candidate.
2. **Raw rows are ephemeral.** They live for an hour by default, then
   they're gone. Moving them to S3 is pointless — they'd be rolled up
   before anyone read them back.
3. **Output JSON is dropped during rollup.** Aggregated rows have
   `Output = {}` (see `processAggregatedResult`). So the columnar layout
   only needs to handle the aggregated metrics jsonb, which is much
   smaller and more regular than raw output.
4. **Read patterns are extremely predictable.** Almost every query is
   `WHERE check_uid = ? AND region = ? AND period_type = ? AND
   period_start BETWEEN a AND b`. That's textbook columnar
   partitioning.
5. **UIDs are UUIDv7.** Sortable by time. Trivially mapped to time-bucketed
   files. The fallback "look up by UID" path mentioned in the schema
   documentation can be served by extracting the timestamp from the UID.

---

## 2. Why columnar object storage at all

If — and only if — this product grows to "many thousands of checks ×
many regions × many years," the rollup retention chain still produces
unbounded `month` rows. With *N* checks × *R* regions × *Y* years × 12
months, you can sketch the worst case:

- 1 000 checks × 5 regions × 5 years × 12 months ≈ 300 K rows. Trivial,
  Postgres yawns.
- 100 000 checks × 10 regions × 10 years × 12 months ≈ 120 M rows. Still
  fine in Postgres if indexed well, but storage cost matters.
- Add `day` retained for 1 year per the same dimensions: 100 K × 10 ×
  365 ≈ 365 M rows. Now the cost story changes.

So the question isn't "do we need cold storage today" (we don't), it's
"do we want to design for the world where we *will* need it."

What columnar object storage buys us:

- **~10× compression on numerics.** Status, period_type, region,
  duration_min/max/p95, availability_pct all encode beautifully —
  low-cardinality dictionaries, run-length, bit-packing.
- **Cheap storage tier.** S3 Standard-IA or even Glacier IR for `month`
  rows that nobody reads more than once a quarter.
- **Pushdown queries.** Read just the columns you want for just the
  bucket you want, without scanning a Postgres table whose hot pages
  are flooded with cold rows.
- **No vacuum pressure.** The PG aggregation job currently churns the
  `results` table constantly (insert + delete). Offloading older
  partitions removes that churn.

That argument is independent of file format. Now the format question.

---

## 3. What Vortex actually is

Vortex is a columnar file format that came out of Spiral DB and is now
in the Apache incubator. Compared to Parquet:

| dimension | Parquet | Vortex |
|-----------|---------|--------|
| age | 2013, ubiquitous | 2024, evolving |
| layout | row groups + dictionary pages | lightweight cascading encodings (FSST, ALP, RLE, FOR, dictionary) over Apache Arrow |
| random access | mediocre — must read footer + dict pages | first-class — designed for "read 100 rows out of a 10M-row file on S3" |
| compute on compressed data | no — must decode pages first | yes — compute kernels operate directly on encoded chunks |
| typical size vs Parquet | baseline | 1.3–2× smaller on time-series workloads (depends heavily on data) |
| typical scan speed | baseline | claimed 2–10× faster for selective reads |
| Go ecosystem | excellent (`parquet-go`, Arrow Go) | usable — `LaurieRhodes/vortex-go` is a Go-native implementation with `io.Reader`/`io.Writer` interfaces (third-party port, maturity TBD) |
| spec stability | frozen | still moving |

The case for Vortex on **this specific workload** is genuinely strong on
paper:

- Heavily skewed floats (`duration_p95`, `availability_pct` heavily
  cluster near small values) → ALP encoding shines (typically 30–50%
  smaller than Parquet PLAIN+SNAPPY).
- Low-cardinality strings (`region`, `period_type`) → dictionary +
  bit-packing, identical between formats but Vortex pays it forward to
  scans.
- Selective queries by check×region×period → Vortex's lazy reading
  avoids the dictionary-page round-trips Parquet needs.
- Statistics embedded per chunk → "what was the worst latency this
  quarter for this check" can be answered without decoding any data
  pages.

If you were starting a greenfield Rust service for time-series storage
today, Vortex would be a defensible bet.

---

## 4. The Go story (revised)

Vortex's reference implementation is Rust, and official bindings exist
for Python (PyO3) and JVM (JNI). The piece that was missing in my
initial analysis: **`github.com/LaurieRhodes/vortex-go` is a Go-native
implementation** that exposes generic `io.Reader` / `io.Writer`
interfaces. Practical implications:

- **Single static binary preserved.** No CGo, no Rust toolchain at
  build time, no glibc coupling, no sidecar process, no network hop on
  the read path.
- **S3 integration is essentially free.** The AWS SDK v2 already
  exposes object bodies as `io.Reader` and accepts `io.Reader` for
  uploads (and supports streaming multipart for large writes). Vortex
  files plug straight in.
- **Cross-compilation still works.** Same `GOOS=linux GOARCH=arm64`
  pipeline, no per-platform Rust toolchain juggling.

So the strongest argument against Vortex specifically — "you'd have to
import a Rust runtime into a Go monoservice" — no longer holds. What
*does* still need due diligence before shipping:

1. **Library maturity.** `vortex-go` is a third-party port, not
   maintained by Spiral / Apache. Before adopting, verify: is it
   actively maintained, does it have releases tagged, does it cover
   read *and* write, which encodings are supported (FSST / ALP / dict /
   RLE / FOR / bitpacked), is there a test suite, is it tracking
   upstream format changes?
2. **Format coverage.** The Vortex spec includes both the in-memory
   array IPC and the on-disk *layout* format (the part that gives
   pushdown and lazy random access on object storage). Confirm the Go
   library supports the layout format — without it, you lose the main
   reason to choose Vortex over Parquet.
3. **Spec drift risk.** Vortex's wire format is still stabilizing in
   the Apache incubator. A file written by `vortex-go` v0.x today must
   still be readable in two years. This is a real risk Parquet doesn't
   carry.
4. **Bug surface.** When something is wrong, you're debugging it
   yourself — there isn't a large user community to file issues against
   yet. Compare to `parquet-go`, which has thousands of production
   users reporting edge cases.

These are due-diligence items, not deal-breakers.

---

## 5. Honest comparison of the alternatives

| approach | ships in Go today | compression vs PG | query story | new infra | when to pick |
|----------|-------------------|-------------------|-------------|-----------|--------------|
| **stay on Postgres** | ✅ | 1× | SQL | none | always, until pain shows up |
| **Postgres + TimescaleDB Hypercore** | ✅ | 5–10× (columnar chunks in-DB) | SQL | Timescale extension | when the only pain is *storage cost* and the team wants zero new systems |
| **Parquet on S3** (with DuckDB or in-process Arrow Go) | ✅ — `parquet-go`, Arrow Go are mature, huge production base | 5–10× | DuckDB SQL or Arrow scans from Go; readable by every BI tool | S3 bucket + lifecycle policies | when you want the safest cold-tier offload with broadest interop |
| **Vortex on S3** (`LaurieRhodes/vortex-go`) | ✅ — pure-Go reader/writer with `io.Reader` interfaces | 7–15× (better encodings on skewed numerics) | direct Go reads; query-tool support thinner than Parquet | S3 bucket + lifecycle policies | when format efficiency matters more than ecosystem breadth, and you accept third-party-library and spec-drift risk |
| **ClickHouse (self-host or Cloud)** | ✅ (HTTP/native client in Go) | 10–20× | ClickHouse SQL | ClickHouse cluster | when this becomes a real metrics product, not a feature |

The trade narrows considerably with `vortex-go` in hand. The remaining
honest comparison between Parquet and Vortex on S3 for this project:

- **Parquet wins on:** ecosystem breadth (every BI tool, every query
  engine, every data team already speaks Parquet), library maturity
  (`parquet-go` and `arrow-go` have been hardened by years of
  production use), and the "zero surprises" guarantee that comes from a
  frozen spec.
- **Vortex wins on:** file size and scan speed for selective queries
  (which is exactly this workload — "give me check X in region Y for
  March"), tighter encoding for skewed floats like `availability_pct`
  and `duration_p95`, and embedded chunk statistics that answer
  min/max-style aggregate queries without decoding any data pages.
- **Tie:** S3 plumbing — both are byte streams against an object
  store, both need similar lifecycle/partitioning thought.

The decision now turns less on "Go support" and more on "are we willing
to take a bet on a third-party port of a still-stabilizing format
because the on-the-wire characteristics fit our data better than
Parquet does." That's a real choice, not a pre-empted one.

---

## 6. What I'd actually do

A staged plan that respects "don't build for hypothetical future
requirements" but doesn't paint into a corner:

### Stage 0 — measure first

Before committing to *any* of this:

- Add a metric: `results` table size per `period_type`, total row count,
  rows added/deleted by the aggregation job per hour.
- Add a query log on the results service to understand actual read
  patterns. My guess is "last 24h hot, last 30d warm, older essentially
  never read" but it's worth verifying.
- If the aggregated rollups never exceed, say, 1 GB per organization,
  this whole conversation is premature.

### Stage 1 — a `ResultStore` abstraction (cheap, useful regardless)

The DB service today is the only path to results. Wrap it behind an
interface in `internal/handlers/results/service.go` that takes the
filter and returns rows. Make Postgres the only implementation today.
This is cheap, makes tests easier, and means the future cold-tier work
is a new implementation, not a rewrite.

### Stage 2 — Cold-tier offload (Parquet *or* Vortex via vortex-go)

If/when the metric from stage 0 shows real growth:

- Define "frozen" as `period_type IN ('day', 'month') AND period_end <
  now() - 30d`.
- Write a job (alongside the existing aggregation job) that exports
  frozen rows to columnar files on S3, partitioned by
  `org/check_uid/year=YYYY/month=MM.<ext>`, then deletes them from
  Postgres.
- The `ResultStore` checks Postgres first; if the requested range
  crosses into the frozen window, it reads the relevant cold files.

The format choice (Parquet vs Vortex) becomes a small, contained
decision because the *abstraction* is the hard part. Recommendation:

1. **Spike both formats** on a real organization's worth of frozen
   rollups. Compare: file size, write throughput, point-read latency
   (single check×region×month), scan throughput (a year of one check),
   and operational glitches. A few days of work, decisive answers.
2. **Default to Parquet unless Vortex's spike numbers are dramatically
   better.** Parquet is the lower-risk choice — broader ecosystem,
   mature library, frozen spec, and it doubles as a free user-facing
   data export (people can `pd.read_parquet` their own monitoring
   data).
3. **Pick Vortex** if the spike shows it's notably smaller / faster
   *and* `vortex-go` looks healthy enough to depend on (active
   maintenance, supports both read and write, supports the layout
   format and not just the IPC shape, has tests). Vortex publishes a
   Parquet → Vortex converter, so a future migration in either
   direction is a batch job, not a rewrite.

### Stage 3 — Pin the bet later, not now

Whichever format ships in Stage 2, the `ResultStore` interface lets us
flip later for cost or performance reasons. Don't paint into a corner;
keep the format choice replaceable.

---

## 7. Things I'd want to nail down before any of this

- **Re-aggregation.** Today the aggregation job is idempotent because
  the source rows still exist before the delete. If we migrate frozen
  data off Postgres and later realize a metric was computed wrong, we
  need to be able to re-derive it. Either keep raw long enough to
  reconstruct (we don't), or accept that Vortex/Parquet files are the
  source of truth for old data.
- **Schema evolution.** The result row has grown columns over time
  (`duration_p95`, `availability_pct` were probably added later). Both
  Parquet and Vortex handle column addition fine, but the read path
  needs to tolerate missing columns in old files.
- **The `metrics` jsonb.** This is the awkward one. Both Parquet and
  Vortex prefer regular schemas. Options: (a) store metrics as a JSON
  string column (works, loses pushdown), (b) flatten to a wide schema
  per check_type (lots of nulls, but each file is per check anyway so
  probably fine), (c) Vortex variant types (powerful but bleeding edge).
- **S3 cost shape.** Per-request cost on small reads is non-trivial.
  Bucket files generously (one file per check per month is probably
  too granular — consider per-org-per-month).
- **Local dev story.** Today `make dev` needs Postgres. If reads can hit
  S3, dev needs MinIO too. Acceptable, but more moving parts.
- **GDPR / deletion.** "Delete this organization's data" today is
  cascade-delete in Postgres. With S3 it's a list-and-delete job — still
  fine but worth designing for.

---

## 8. Honest one-paragraph verdict (revised)

Vortex is technically the right shape for this data — columnar,
cloud-native, lazy, well-encoded for skewed numerics and low-cardinality
strings — and with `vortex-go` providing a pure-Go reader/writer that
plugs straight into S3 via standard `io.Reader` / `io.Writer`, the
"wrong language" objection that dominated my first take is gone.
**The remaining honest tradeoff is: a third-party port of a still-
stabilizing format with better encoding, vs. a battle-tested format
with a vast ecosystem.** I still wouldn't ship either today (the
storage pain is hypothetical), but the right plan is now: build the
`ResultStore` abstraction, instrument actual growth, then when the
numbers justify it, spike both formats on real data and pick the
winner. Vortex is no longer a "later, maybe never" option — it's a
defensible "later, possibly first-choice" one.
