---
model: opus
effort: medium
---

# Nothing tells a user which regions can actually do IPv6 until their check has already failed

## Problem

`ipVersion: ipv6` is selectable on every check, in every region, whether or not
the workers in that region have an IPv6 route. The failure is handled well but
**reactively**: the user creates the check, waits for the first run, and gets an
error telling them to pick a different region.

The reactive half is already right, and this spec does not touch it.
[`ipversion.go:386`](../../../server/internal/checkers/checkerdef/ipversion.go:386)
`DialEgressProbe` connects a UDP socket (route lookup only, no packet sent) and
turns `ENETUNREACH`/`EAFNOSUPPORT` into
[`ErrWorkerNoEgress`](../../../server/internal/checkers/checkerdef/ipversion.go:78) —
*"this worker has no IPv6 connectivity, so the check could not even be sent —
the target is probably fine; run this check from a region with IPv6 egress"*.
That is the correct answer to "did my target go down or did our worker fail",
and it keeps a v4-only worker from reporting a false DOWN.

What is missing is the **proactive** half:

- [`regions.go:16`](../../../server/internal/regions/regions.go:16) —
  `RegionDefinition{Slug, Emoji, Name}`. No capability information at all, so
  the region picker cannot mark, sort, or filter on IPv6.
- [`worker.go:10`](../../../server/internal/db/models/worker.go:10) —
  `Worker{UID, Slug, Name, Region, LastActiveAt, …}`. A worker never reports
  what it can reach, so the server could not answer the question even if the
  API wanted to.
- [`postgres.go:1258`](../../../server/internal/db/postgres/postgres.go:1258)
  `RegisterOrUpdateWorker` — registration carries slug/name/region and refreshes
  `last_active_at`. The natural place to carry a capability, and the natural
  place to keep it fresh.

Three consequences, worst first:

1. **Private locations (deported agents) fail silently in the worst way.** A
   customer installs an agent on their own host to monitor an internal service,
   pins `ipv6`, and the agent's host has no v6 route — common on a
   default-configured VPS or a v4-only corporate LAN. They see failing checks
   on infrastructure they control, and the natural reading is "your product is
   broken", not "my host has no IPv6". They cannot compare against another
   region, because their private region is the only one that can see the target.
2. **It costs us the differentiator at exactly the moment it should earn.**
   IPv6 monitoring is a deliberate selling point and several competitors handle
   v6 poorly. A prospect evaluating it discovers our v6 support by getting an
   error on their first v6 check. The capability is real — the presentation
   makes it look absent.
3. **The pre-flight is per-run.** Every explicit-family check pays a route
   lookup on every execution to re-derive a fact that changes approximately
   never. Cheap (microseconds), but it is also the reason the answer is only
   ever available *after* a run.

## Proposal

Have workers report their egress families, aggregate to the region, and use it
in the two places a user makes the decision: picking a region, and pinning a
family.

The existing per-run probe **stays exactly as it is**. Advertised capability is
a hint for the UI, never a gate on execution — a worker whose v6 route came back
must not be blocked by a stale flag, and one whose route went away must still
produce `ErrWorkerNoEgress` rather than a false DOWN. Same principle the probe's
own comment states: *"the probe only ever upgrades an error message, never
downgrades one."*

### 1. Workers report egress families

At registration and at each heartbeat, a worker probes its own egress — reuse
`DialEgressProbe` against a well-known off-link address per family (no packet
sent, no dependency on that address being reachable) — and reports
`{ipv4: bool, ipv6: bool}`.

Probed at report time, not at process start: a worker that gains or loses v6
converges within one heartbeat instead of requiring a restart.

**Not on the self-stats channel.** Workers already self-report via
`setupSelfStats` ([checkworker/worker.go:1467](../../../server/internal/checkworker/worker.go:1467),
spec `2025-12-28-worker-self-stats`), and that is the obvious place to put this —
but it writes time-series rows through the results infrastructure. Capability is
current-state metadata that has to be joined per region on every region-list
read; answering "can region X do v6" by scanning a results stream would be the
wrong shape. It belongs on the worker row, next to `last_active_at`, which is
already the freshness signal the aggregation depends on.

### 2. Store it on the worker

`Worker.EgressIPv4` / `Worker.EgressIPv6` as nullable booleans, refreshed by
`RegisterOrUpdateWorker` alongside `last_active_at`.

**Nullable, and null means "unknown".** An older agent that does not report is
not the same as an agent with no v6, and must never be rendered as one — this is
the migration-safety property that makes the rollout order irrelevant.

### 3. Aggregate to the region

A region advertises a family when **at least one live worker** in it
(`last_active_at` within the existing liveness window) reports that family.
Any-not-all, because a job lands on one worker: if some worker there can do v6,
a v6 check in that region can succeed.

Serve it on `RegionDefinition` as `capabilities: {ipv6: "yes"|"no"|"unknown"}`.
A three-state value, not a bool — "we do not know" is a real state (no live
workers, or none reporting yet) and collapsing it to false is exactly the lie
this spec exists to stop telling.

### 4. Use it where the user decides

- **Region picker**: mark v6-capable regions; when the check is pinned to
  `ipv6`, sort those first and visually de-emphasise the rest. Never hide a
  region — an `unknown` region may be perfectly capable.
- **Check validation**: pinning `ipv6` in a region advertising `no` returns a
  **warning, not a rejection** — the region may have gained v6 since the last
  heartbeat, and the run-time probe is the authority. The warning names the
  region and points at one that advertises `yes`.
- **Private locations**: surface it on the location's own page — this is the
  case where the user can actually fix it, by enabling IPv6 on the host they
  own.

### Non-goals

- Gating execution on advertised capability. Stated twice on purpose: the
  advertised value is a hint, the probe is the authority.
- Probing the target's families. This is about the worker's egress.
- Per-check-type capability. `SupportsIPVersion` metadata already covers which
  types honour a pinned family (`ipversion_test.go:479`) and is orthogonal.
- Reporting anything else about the worker (bandwidth, DNS, geo). One
  capability, one spec — the field is a map so the next one is additive.

## Acceptance criteria

1. A worker with v6 egress reports `ipv6: true`; a v4-only worker reports
   `ipv6: false`; both survive a heartbeat cycle without flapping.
2. A worker that never reports leaves the columns null, and its region
   advertises `unknown` — not `no`.
3. A region with one v6 worker and three v4-only workers advertises `yes`.
4. A region whose only v6 worker has gone stale (past the liveness window)
   stops advertising `yes`.
5. The region list API returns the capability; an older client ignoring the
   field behaves exactly as today.
6. Creating an `ipv6` check in a `no` region succeeds with a warning naming the
   region.
7. A check pinned to `ipv6` in a region that advertises `no` but does in fact
   have v6 **runs and passes** — the advertised value never blocks execution.
8. A worker that loses its v6 route between heartbeats still produces
   `ErrWorkerNoEgress`, not a DOWN (existing behaviour, pinned against
   regression).
9. A private location with no v6 shows the capability on its own page.

## Implementation Plan

### 1. Egress self-probe

`checkerdef` (or a thin wrapper beside it): `ProbeEgress() map[IPVersion]bool`,
built on `DialEgressProbe` so there is one route-lookup implementation. Called
by the worker at report time.

### 2. Model + migration

`Worker.EgressIPv4` / `EgressIPv6` `*bool`; migration adds two nullable boolean
columns, no backfill (null is the honest value for every existing row).

### 3. Registration

`RegisterOrUpdateWorker`
([postgres.go:1258](../../../server/internal/db/postgres/postgres.go:1258) and
the sqlite twin at
[sqlite.go:1236](../../../server/internal/db/sqlite/sqlite.go:1236)) accepts and
refreshes the columns. Both backends, or the sqlite dev path drifts.

### 4. Region aggregation

`regions.Service`: compute capabilities from live workers per region, for global
and org-custom regions alike
([regions.go:155](../../../server/internal/regions/regions.go:155),
[regions.go:328](../../../server/internal/regions/regions.go:328)). Private
regions carry the `@` prefix and are org-scoped — every resolution path must
filter by `organization_uid`, per the warning at
[regions.go:23](../../../server/internal/regions/regions.go:23) and
`wiki/conventions/regions.md`. Add `Capabilities` to `RegionDefinition` as an
omit-empty map so stored region JSON stays readable by an older binary.

### 5. API + UI

Region list response; region picker marking and sort-on-pin; the validation
warning; the private-location page. OpenAPI updated.

### 6. Tests

- Probe: v6-capable and v4-only hosts (inject the probe, as
  `SelectIPAddrWithProbe` already allows — this is why that seam exists).
- Aggregation: any-not-all; stale worker excluded; no workers → `unknown`;
  null-reporting worker → `unknown`, never `no`.
- Private-region aggregation does not leak across orgs (the `@`-prefix trap).
- Validation returns a warning, and the check is still created and still runs.
- Regression: advertised `no` + real v6 → check passes.

### 7. Docs

`wiki/features/deported-agents.md` — how to give an agent IPv6 and how to read
the capability. A docs page on IPv6 monitoring is worth having for its own sake:
it is a differentiator we currently document mainly through an error message.

## Related

- [`2025-12-28`](../done/2025/12/2025-12-28-unified-url-handling.md) and the
  per-checker `ipversion_test.go` suite — the family-pinning behaviour this
  builds on.
- `wiki/features/deported-agents.md` — private locations, the worst-affected
  case.
- `wiki/conventions/regions.md` — the `@`-prefixed private-region rules every
  aggregation path has to honour.
