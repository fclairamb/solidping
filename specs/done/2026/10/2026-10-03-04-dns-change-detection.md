---
model: opus
effort: medium
---

# DNS check: detect record changes against a captured baseline

## Problem

The `dns` check can only compare answers against values the user types in
(`expected_ips`, `expected_values`, `checkers/checkdns/config/config.go`). The
match is a subset test: every expected value must be present, extra values are
ignored (`matchValues`, `checkers/checkdns/checker.go:298`). So the check
catches "my record disappeared" but not:

- a nameserver delegation changed (`NS`), the classic sign of a hijack or an
  expired domain being taken over;
- a new `MX` or a changed `TXT` (SPF, DMARC, domain verification);
- a `CNAME` repointed by someone with registrar or DNS console access.

Users also have to know and type the current values first, which few do. Oh Dear
sells "DNS monitoring" as change detection on every plan
(`wiki/competitors/ohdear.md`). We have the probe; we lack the baseline.

## Proposal

Add an opt-in mode to the existing `dns` check: on the first successful run the
server **captures the answer as a baseline**, and every later run compares the
full answer set with it. Any difference (added or removed value) is reported.
The baseline lives **in the check config**, so it is visible in the dashboard,
exported by `sp export`, and travels to private agents with the job config like
any other field. The checker stays stateless.

(Considered and rejected: storing the baseline with the multi-step state rail of
spec 2026-10-03-03. That rail is cloud-worker only, and a DNS check must keep
running on private agents.)

### 1. Config

`checkers/checkdns/config/config.go`, `DNSConfig`, three new fields (snake_case
like the existing ones):

| Field | Type | Default | Meaning |
|---|---|---|---|
| `detect_changes` | bool | false | Turn the mode on. |
| `baseline` | `map[string][]string` | empty | Region → normalized values. Filled by the server, editable by the user. |
| `on_change` | `"down"` / `"warning"` | `"down"` | Status when the answer differs from the baseline. |

- Parse in `FromMap`, emit in `GetConfig` (omit when empty / default), same
  style as `expected_values`.
- `config/validate.go`: `on_change` in the closed set; `baseline` only when
  `detect_changes` is true; at most 100 values per region; region keys
  non-empty.
- Keyed by region because GeoDNS answers differ by region by design. One shared
  baseline would flag every multi-region check on a CDN-hosted name.

### 2. Region selection (generic hook)

Checkers don't know their region today. Add an optional config interface in
`checkers/checkerdef/interface.go`:

```go
// RegionSelector is implemented by configs that hold per-region data. The
// worker calls it after parsing, with the job's region, before Execute.
type RegionSelector interface {
    SelectRegion(region string)
}
```

Call it in `executeJob` (`checkworker/worker.go:921`) after the config is
parsed, with `resolveResultRegion(checkJob)` (`:1570`), the same value the
result row gets. `DNSConfig.SelectRegion` stores the region and the matching
baseline slice in unexported fields. It's the same parse-then-probe pattern as
`BurstBudgeter` / `ExtraBudgeter`.

### 3. Comparison in the checker

In `Execute` (`checkers/checkdns/checker.go:41`), after the existing expected-value
checks (`:132-145`) and only when `detect_changes` is true:

1. **Normalize** the answer: lower-case, strip a trailing dot, trim spaces,
   dedupe, sort. Same function used for the stored baseline. TXT keeps case but
   is still deduped and sorted.
2. **No baseline for this region, and the lookup succeeded:** status unchanged,
   and `Output["baseline_capture"] = <normalized values>`. The server stores it
   (step 4). NXDOMAIN, timeouts and errors never produce a capture.
3. **Baseline present:** compute `added` (in answer, not in baseline) and
   `removed` (in baseline, not in answer). If either is non-empty:
   - status = `StatusDown` (`on_change: down`) or `StatusWarning`
     (`on_change: warning`; amber, counts as up, no incident,
     `checkerdef/expiry.go:9`);
   - `Output["changes"] = {"added": [...], "removed": [...]}`;
   - `Output[OutputKeyError] = "DNS records changed: +N −M"` when down, without
     overwriting the existing "resolved values do not match expected values"
     message if that also failed.
4. `Metrics["changed"] = 0|1` for the history charts.

`expected_*` and `detect_changes` stay independent: both may be set, and the
worse status wins.

### 4. Capturing the baseline (server side)

When a result for a `dns` check carries `Output["baseline_capture"]`, the server
writes it into `checks.config.baseline[<region>]`:

- **Only if that region has no baseline yet.** Two regions capturing at the
  same moment must not overwrite each other: do a single-key update
  (`jsonb_set` on PG, `json_set` on SQLite) guarded by "key absent", or a
  read-modify-write under a row lock in one transaction. **Never** a whole-config
  rewrite from a stale copy.
- Propagate to `check_jobs.config` the same way a config PATCH does (the jobs
  hold a materialized copy).
- Record an audit event ("DNS baseline captured", region, value count) through
  the existing events system.
- Hook it in the server-side result path shared by in-process workers and agent
  results, not in `DirectBackend` alone, so agent-run DNS checks also capture.

### 5. Accepting a change

No new endpoint. **"Accept current records" = PATCH the check with
`baseline: {}`**, then trigger a run. The next run of each region captures
again (step 3.2). In the dashboard:

- Check form: a "Detect changes" toggle, the `on_change` select, and the stored
  baseline shown read-only per region with a "Reset baseline" action.
- Check detail, when the latest result has `changes`: show added and removed
  values, and an "Accept current records" button doing the PATCH + run now.
- EN and FR strings.

Config-as-code: a PATCH or `sp apply` that **omits** `baseline` keeps the stored
one; an explicit `{}` clears it. Otherwise every `apply` of a manifest exported
before capture would silently reset the baseline.

### 6. Samples

Add one sample in `checkers/checkdns/samples.go`: NS change detection on a
domain (`record_type: NS`, `detect_changes: true`). NS is the most valuable
record to watch and the least prone to false positives.

## Tests

- `checkers/checkdns/config` tests: `FromMap` / `GetConfig` round-trip of the
  three fields; validation refuses an unknown `on_change`, a baseline without
  `detect_changes`, more than 100 values, an empty region key.
- `checkers/checkdns/checker_test.go` (stub resolver):
  - no baseline + success → `baseline_capture` present, status up;
  - no baseline + NXDOMAIN / timeout → no `baseline_capture`;
  - same values in another order, case or trailing dot → no change;
  - one value added → `changes.added`, status down; one removed →
    `changes.removed`, status down;
  - `on_change: warning` → `StatusWarning`;
  - `expected_values` failing and a change at the same time → down, the
    expected-values error message is kept;
  - `SelectRegion` picks the right region's baseline; a region absent from the
    map captures.
- `checkworker/worker_test.go`: a config implementing `RegionSelector` receives
  the job's resolved region before `Execute`.
- Server-side capture tests (PG and SQLite):
  - first capture writes `baseline[region]` and the check job's config;
  - an existing region baseline is never overwritten by a later capture;
  - **two regions capturing concurrently both end up stored**;
  - an audit event is recorded;
  - a capture from an agent-submitted result is stored too.
- `handlers/checks` tests: PATCH without `baseline` keeps it; PATCH with
  `baseline: {}` clears it.

## To verify

- The server-side function that processes submitted results for both in-process
  and agent workers (where the capture hook goes). `DirectBackend.SubmitResult`
  is at `checkworker/backend/direct.go:221`; the agent path ends somewhere under
  `handlers/agentws`.
- Whether `UpdateCheck` (`handlers/checks/service.go:2023`) replaces or merges
  `config`, to implement the "omitted `baseline` is kept" rule at the right
  level.
- How a config change is propagated to `check_jobs.config` today, to reuse it
  for the capture write.
- The events/audit API for a server-originated event (no acting user).
- How `lookupMX` and `lookupTXT` (`checker.go:250`, `:285`) format their values,
  so normalization does not merge two distinct records.

## Open questions

1. **Default `on_change` for `A` / `AAAA`.** Names behind load balancers and
   CDNs rotate their A records, which would fire on every rotation.
   Recommended: keep `down` as the default for every record type, but have the
   form preselect `warning` when the user turns detection on for `A`/`AAAA`, with
   a one-line hint explaining why.
2. **Should a change auto-resolve when the old values come back?** As specified,
   yes: the check goes back up when the answer matches the baseline again, like
   any other check. Recommended: keep it that way, so the baseline only moves
   when a user accepts the change.

## Resolved open questions

Answered unattended on 2026-10-03 with the spec's recommended answers; to be reviewed by the owner.

1. Keep `down` as the backend default `on_change` for every record type. When the user turns detection on for `A`/`AAAA`, the form preselects `warning` and shows a one-line hint about rotating load-balancer/CDN records.
2. A change auto-resolves: the check goes back up when the answer matches the baseline again. The baseline only moves when a user accepts the change.

## Out of scope

- `SOA` serial monitoring. `SOA` is listed as a valid record type
  (`config/recordtypes.go`) but `lookupSOA` returns `errSOANotSupported`
  (`checker.go:290`). `github.com/miekg/dns` is already a dependency, so a
  real SOA lookup is a small follow-up, and a changed serial is a good "zone
  edited" signal.
- Watching several record types in one check. One check, one record type,
  as today.
- DNSSEC validation.
