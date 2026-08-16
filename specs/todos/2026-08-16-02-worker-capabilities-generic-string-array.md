---
model: opus
effort: high
---

# Worker capabilities are two hard-coded boolean columns; they should be one generic string array

## Problem

Spec 2026-08-15-11 landed per-worker egress capability as **two dedicated
boolean columns**, `workers.egress_ipv4` and `workers.egress_ipv6`
([013_v0_16_0.up.sql](server/internal/db/postgres/migrations/013_v0_16_0.up.sql)),
surfaced as `*bool` pairs all the way up the stack:

- [worker.go:24](server/internal/db/models/worker.go:24) — `EgressIPv4` / `EgressIPv6 *bool`
- [worker.go:35](server/internal/db/models/worker.go:35) — the `WorkerEgress` carrier struct
- [protocol.go:56](server/internal/agents/protocol.go:56) — the wire fields `egressIpv4` / `egressIpv6`
- [postgres.go:1293](server/internal/db/postgres/postgres.go:1293), [sqlite.go:1256](server/internal/db/sqlite/sqlite.go:1256) — per-column upsert branches
- [capabilities.go](server/internal/regions/capabilities.go) — `aggregateIPv6` folds workers into a region verdict

The shape does not match the concept. `internal/regions` already models
capabilities as an open **map keyed by name**, and says so explicitly at
[capabilities.go:12](server/internal/regions/capabilities.go:12): *"A map key
rather than a struct field so the next capability is purely additive on the
wire."* The region layer is generic; the storage and transport layers under it
are not. Every new capability — say `ipv6`, `udp`, `icmp`, `tls13`,
`http3` — currently costs a schema migration, two new nullable columns, two new
upsert branches per dialect, a new wire field, and a new aggregation function.
That is the additive-ness the region layer promised and the layers below
cannot deliver.

The columns are also **unshipped**. The latest tag is `v0.15.1` (with `v0.15.2`
in flight), and this migration is `013_v0_16_0` — the pending migration for the
*next* release. No released database has ever applied it, so the shape can
still be fixed in place rather than being frozen and worked around forever.
This window closes when v0.16.0 is cut.

### The trap: three states, not two

The hard part is that `*bool` here is **deliberately three-state**, and the
existing code comments defend that choice at length
([worker.go:16-23](server/internal/db/models/worker.go:16),
[capabilities.go:21-33](server/internal/regions/capabilities.go:21),
[013_v0_16_0.up.sql](server/internal/db/postgres/migrations/013_v0_16_0.up.sql)):

- `nil` — **unknown**: no worker reported, or the agent predates the feature
- `true` — **yes**
- `false` — **no**

`unknown` must never collapse into `no`. A region served by an agent that
predates capability reporting would otherwise be advertised as IPv6-incapable,
and users would avoid regions that work fine. That three-state property is the
entire reason spec 2026-08-15-11 exists, and it is also what makes the
server-first/agent-first rollout order irrelevant.

A naive `capabilities text[]` regresses exactly this. With a single array,
"absent from the list" has to mean *either* unknown *or* no, and the two become
indistinguishable — silently reintroducing the lie the previous spec was
written to remove. Any proposal here has to carry three states, not two.

## Proposal

Replace both boolean columns with **one nullable array column**,
`workers.capabilities`, holding the set of capability names the worker reports
it *has*. Three states are preserved by distinguishing NULL from empty:

| Column value | Meaning | Aggregation contribution |
|---|---|---|
| `NULL` | **unknown** — nothing ever reported | none, in either direction |
| `{}` (non-null, empty) | reported, and has none of them | `no` for every capability |
| `{"ipv4","ipv6"}` | reported this exact set | `yes` for members, `no` for non-members |

The rule is: **NULL is the only unknown.** Once a worker reports at all, its
array is authoritative and closed — absence from a non-NULL array means "no",
never "unknown". This keeps the three states that
[capabilities.go:21](server/internal/regions/capabilities.go:21) documents while
making every future capability a pure string addition with no migration.

### Enforce the shape in the migration

Rewrite `013_v0_16_0` **in place** (see the mechanics section — do not add a
`014`), dropping the two boolean columns in favour of the array, and constrain
it at the DB level rather than trusting the writers:

- **Postgres**: `capabilities text[]`, nullable, with a `CHECK` that rejects
  NULL elements, empty-string elements, duplicates, and anything outside the
  capability-slug charset (lowercase `[a-z0-9-]+`). `array_position(capabilities, NULL) IS NULL`
  covers the NULL element case; a cardinality-vs-distinct comparison covers duplicates.
- **SQLite**: no array type, so `capabilities text` holding a JSON array, with a
  `CHECK (capabilities IS NULL OR (json_valid(capabilities) AND json_type(capabilities) = 'array'))`
  plus the same element constraints expressed over `json_each`. The two
  dialects must agree on what they reject — a value the Postgres constraint
  refuses must not be storable in SQLite.

Enforcing in the schema is the point of the change: the whole risk of moving
from typed columns to a stringly-typed set is that garbage becomes
representable, and a `CHECK` is what buys back the safety the `boolean` type
used to give for free.

### Layers to move

1. **Model** — replace `EgressIPv4`/`EgressIPv6 *bool` with
   `Capabilities []string` (nil slice = unknown, non-nil = reported set) on
   [Worker](server/internal/db/models/worker.go:10). Replace the `WorkerEgress`
   carrier and its `Empty()` with a capability-set equivalent; keep a helper that
   answers "does this worker report capability X" as a tri-state, so callers
   cannot accidentally treat unknown as no.
2. **Wire** — replace `egressIpv4`/`egressIpv6` on the claim frame
   ([protocol.go:56](server/internal/agents/protocol.go:56)) with a single
   optional `capabilities []string`. The field stays optional in both
   directions: an agent that omits it leaves the column NULL. **Verify the
   assumption that no released agent sends the old fields** — they appear to be
   unshipped alongside the migration, which would make a compatibility shim
   unnecessary; if any released agent does send them, keep decoding them into
   the array for one release instead.
3. **Persistence** — collapse the per-column upsert branches at
   [postgres.go:1293](server/internal/db/postgres/postgres.go:1293) and
   [sqlite.go:1256](server/internal/db/sqlite/sqlite.go:1256) into a single
   `capabilities` write, preserving the existing "nil means do not touch the
   stored value" semantics so a heartbeat that reports nothing never overwrites
   a known set.
4. **Aggregation** — generalise `aggregateIPv6`
   ([capabilities.go:56](server/internal/regions/capabilities.go:56)) into an
   `aggregate(workers, capabilityName)` that keeps the current ANY-not-ALL rule
   and the unknown/no/yes verdicts. The region-facing
   `map[string]string` of `yes`/`no`/`unknown` and `IPv6Capability()` are the
   public contract and should **not** change shape — this is a storage and
   transport refactor, and the API response for regions must stay byte-identical.
5. **Producer** — the in-process worker's self-probe
   ([worker.go:356](server/internal/checkworker/worker.go:356)) and the agent
   claim handler ([handler.go:456](server/internal/handlers/agentws/handler.go:456))
   emit the set instead of two booleans.

### Migration mechanics (read before editing 013)

Per `server/CLAUDE.md` and the repo's migration rules:

- **Edit `013_v0_16_0` in place; do NOT renumber and do NOT add a `014`.** Bun
  keys applied migrations on the numeric prefix alone, so a renumber makes every
  migrated database treat it as new or silently skip a real one.
- Update **both dialects and both directions** together —
  `postgres/013_v0_16_0.{up,down}.sql` and `sqlite/013_v0_16_0.{up,down}.sql`.
- **Dev databases have already applied 013** (the local dev DB and the
  `solidping-dev` k8xp deployment). Because bun matches on the prefix, the
  rewritten 013 will **not** re-run there: those databases keep the old boolean
  columns while the new code expects `capabilities`, which surfaces as a startup
  crash or 500s. The dev DB must be reset (`SP_DB_RESET`) or patched by hand and
  its `bun_migrations` row reconciled. Call this out in the PR description so
  the k8xp dev deployment is reset rather than debugged.
- Grep for the migration filename before touching it — migration tests read
  these files by name via `migrationsFS.ReadFile`.

### Tests

The audit-worthy risk is a silent regression from three states to two, so the
tests must prove the negative rather than just exercise the happy path:

- A worker with `capabilities = NULL` aggregates to `unknown`, **not** `no` —
  the direct regression guard for spec 2026-08-15-11.
- A worker with `capabilities = '{}'` aggregates to `no`, proving NULL and
  empty are genuinely distinguishable end to end (not merely in Go).
- A heartbeat carrying no capabilities leaves a previously-reported set intact.
- Positive control: a worker reporting `{"ipv6"}` yields `yes`, so the
  unknown/no assertions above are not passing vacuously.
- Migration-level: the `CHECK` constraint actually rejects duplicates, empty
  strings, NULL elements and out-of-charset names — on **both** dialects, with
  the same verdict.
- The regions API response for a given fixture is unchanged versus the current
  boolean implementation.

## Open questions

- Should `ipv4` be reported at all? Today `egress_ipv4` is stored but nothing
  aggregates it — only IPv6 has a verdict. The array makes carrying it free, so
  the default is to keep reporting it, but confirm no reader is expected to
  start branching on it as part of this change.
- Is a capability name ever going to need a *value* (e.g. `tls: 1.3`) rather
  than mere presence? If so a `text[]` set is the wrong end state and a JSONB
  map would be. The assumption here is presence-only, matching the existing
  `map[string]string` of yes/no/unknown verdicts.

## Resolved open questions

> Should `ipv4` be reported at all? Today `egress_ipv4` is stored but nothing
> aggregates it — only IPv6 has a verdict. The array makes carrying it free, so
> the default is to keep reporting it, but confirm no reader is expected to
> start branching on it as part of this change.

**Decision: keep reporting `ipv4` AND add an `ipv4` verdict to the region
aggregation.** Producers emit `ipv4` alongside `ipv6` in the capabilities array,
and the generalised `aggregate(workers, capabilityName)` is called for **both**
`ipv4` and `ipv6`, so the region-facing `map[string]string` carries an `ipv4`
entry of `yes`/`no`/`unknown` on the same ANY-not-ALL rule and the same
three-state semantics as `ipv6`. Expose an `IPv4Capability()` accessor mirroring
the existing `IPv6Capability()`.

**This deliberately relaxes the "the API response for regions must stay
byte-identical" constraint in the Proposal's *Aggregation* section (layer 4) —
that sentence no longer holds and is superseded by this decision.** The relaxation
is *narrowly scoped*, and the implementation must not widen it:

- The regions API response gains **exactly one new key**, `ipv4`, in the
  capabilities map.
- **Every pre-existing key keeps its exact current name, value domain and
  computed value** — in particular `ipv6` must be bit-for-bit what the boolean
  implementation produced for the same fixture. Adding `ipv4` is not licence to
  rename, restructure, or re-derive anything else.
- The last bullet of the *Tests* section is amended accordingly: instead of
  "unchanged versus the current boolean implementation", assert that the regions
  API response for a given fixture is **unchanged except for the addition of the
  `ipv4` key**, and add a case proving `ipv4` itself carries all three states
  (NULL → `unknown`, `{}` → `no`, `{"ipv4"}` → `yes`). The unknown-is-not-no
  regression guard applies to `ipv4` exactly as it does to `ipv6`.

> Is a capability name ever going to need a *value* (e.g. `tls: 1.3`) rather
> than mere presence? If so a `text[]` set is the wrong end state and a JSONB
> map would be. The assumption here is presence-only, matching the existing
> `map[string]string` of yes/no/unknown verdicts.

**Decision: presence-only.** Implement the set exactly as specced — Postgres
`text[]`, SQLite JSON array of names — with the `CHECK` constraints described
above. Do **not** reach for JSONB or any key/value shape. If a capability ever
genuinely needs a value, encode it as a slug within the existing charset (e.g.
`tls13`) or migrate to a richer shape at that point; that is explicitly out of
scope here.
