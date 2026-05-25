# Discovery: Freebox active ICMP + shared port scan (backend)

## Context

The discovery feature has two scan methods. The **LAN scan** actively ICMP-pings
and TCP-port-scans each host in a CIDR range, giving every result real
`icmpReachable`, `open_ports`, and `suggested_checks`. The **Freebox scan** is a
passive lookup: it calls the paired Freebox router's LAN browser API
(`GET /api/v4/lan/browser/pub/`), copies the router's own `reachable` flag as
`icmpReachable`, and hardcodes `open_ports`/`suggested_checks` to `"[]"` — no
real probing at all (see
[`server/internal/jobs/jobtypes/job_freebox_lan_discovery.go:107-109`](../../server/internal/jobs/jobtypes/job_freebox_lan_discovery.go)).

This makes Freebox-discovered hosts second-class citizens: they have no open
ports, no suggested checks, and their "reachable" flag is the router's cached
view, not a live probe. Users who promote a Freebox host have no port-based
check suggestions to work from.

There is a secondary code-quality problem: the port list exists as **two
independent literals** that must be kept in sync manually:

- `defaultPortList()` in
  [`server/internal/discovery/scanner.go:26-29`](../../server/internal/discovery/scanner.go)
  — the ports the scanner actually probes.
- `suggestForPort` switch in
  [`server/internal/discovery/suggest.go:37-57`](../../server/internal/discovery/suggest.go)
  — the port → suggested-check-type/URL mapping. This implicitly encodes the
  same port set; a port added to the scanner but missing from this switch would
  silently produce no suggestion.

## Goal

1. After fetching the Freebox device list, actively ICMP-ping and TCP-port-scan
   each reported host using the **same scanner engine** the LAN scan uses, so
   Freebox hosts get identical-quality `icmpReachable`, `open_ports`, and
   `suggested_checks`.
2. Preserve the Freebox-provided hostname (device name from the router) — prefer
   it over reverse DNS.
3. Unify the port list into one source of truth shared by both the scanner and
   the suggestion engine, eliminating the drift hazard.

## Non-goals

- Merging the two job types (`network_discovery` / `freebox_lan_discovery`) or
  their backend endpoints into one.
- Changing the LAN scan behavior, the CIDR safety cap (`safety.go`), or the
  `MaxAddresses = 4096` limit.
- Exposing per-scan advanced overrides (ports/timeout/concurrency) in the
  Freebox scan's API — it inherits scanner defaults.
- Changing the Freebox pairing flow or any other Freebox integration.
- Frontend changes (covered by spec `2026-05-25-09`).

## Shared port table

Define one authoritative port table in `server/internal/discovery/ports.go` (new
file, internal to the `discovery` package):

```go
type portSpec struct {
    Port      int
    CheckType string // "http" or "tcp"
    URLTmpl   string // only for http; empty for tcp
}

var defaultPorts = []portSpec{
    {22,   "tcp",  ""},
    {25,   "tcp",  ""},
    {53,   "tcp",  ""},
    {80,   "http", "http://%s"},
    {110,  "tcp",  ""},
    {143,  "tcp",  ""},
    {443,  "http", "https://%s"},
    {465,  "tcp",  ""},
    {587,  "tcp",  ""},
    {993,  "tcp",  ""},
    {995,  "tcp",  ""},
    {3306, "tcp",  ""},
    {5432, "tcp",  ""},
    {6379, "tcp",  ""},
    {8080, "tcp",  ""},
    {8443, "tcp",  ""},
}

func defaultPortList() []int {
    ports := make([]int, len(defaultPorts))
    for i, p := range defaultPorts { ports[i] = p.Port }
    return ports
}
```

Replace `defaultPortList()` in `scanner.go` (currently lines 26-29) with a call
to this function, and rewrite `suggestForPort` in `suggest.go` (lines 37-57) to
range over `defaultPorts` instead of a switch, producing the same output.

## Explicit-host scan API

Extend `server/internal/discovery/scanner.go`:

```go
// HostInput is one host to probe, with an optional pre-resolved name.
type HostInput struct {
    IP           net.IP
    HostnameHint string // preserved over reverse-DNS if non-empty
}

// ScanHosts probes a fixed list of IPs through the same engine as Scan.
// cfg.Ports defaults to defaultPortList(); cfg.Cidrs is ignored.
func ScanHosts(ctx context.Context, hosts []HostInput, cfg Config) ([]Result, error)
```

Implementation: extract the inner worker-pool loop that `Scan` uses after CIDR
expansion into a shared private function `runProbes(ctx, ips []probeTarget, cfg)`.
`Scan` calls it after expansion; `ScanHosts` calls it with the pre-supplied list.
`probeTarget` carries `IP` + `HostnameHint`; `probeHost` sets `result.Hostname`
from the hint when non-empty, else from `resolveHostname`.

## Freebox job rewrite

[`server/internal/jobs/jobtypes/job_freebox_lan_discovery.go`](../../server/internal/jobs/jobtypes/job_freebox_lan_discovery.go)

Current flow:
1. Call `freebox.ListLanHostsForChannel(...)` → `[]freebox.LanHost`
2. Persist each host with `open_ports: "[]"` and `suggested_checks: "[]"`.

New flow:
1. Call `freebox.ListLanHostsForChannel(...)` → `[]freebox.LanHost`
2. Build `[]discovery.HostInput` from the returned devices (keep the existing
   filter that drops routers and hosts with no active IPv4).
3. Call `discovery.ScanHosts(ctx, inputs, discovery.Config{})` — inherits
   default port list, default timeout (1 s), default concurrency (64).
4. Merge results: for each `Result`, if the corresponding `LanHost` had a
   non-empty device name, override `Result.Hostname` with it. (Or pass the name
   in `HostInput.HostnameHint` and let `ScanHosts` handle it.)
5. Call `discovery.SuggestChecks(results)`.
6. Persist with `source = models.DiscoverySourceFreebox` and the real
   `open_ports`/`suggested_checks` from the scan.

The existing upsert conflict key `(organization_uid, ip, source)` and the
`checkAlreadyRunning` guard are unchanged — no migration required.

## Files affected

| File | Change |
|---|---|
| `server/internal/discovery/ports.go` | New — authoritative port table + `defaultPortList()` |
| `server/internal/discovery/scanner.go` | Add `HostInput`, `ScanHosts`; remove `defaultPortList()` (moved); extract inner loop |
| `server/internal/discovery/suggest.go` | Replace `suggestForPort` switch with range over `defaultPorts` from `ports.go` |
| `server/internal/jobs/jobtypes/job_freebox_lan_discovery.go` | Rewrite scan loop to call `ScanHosts` + `SuggestChecks`; remove `"[]"` literals |

No API, migration, model, or frontend changes.

## Verification

1. **`make build && make lint && make test`** — must pass clean.
2. **Unit test for `ScanHosts`**: given a list of `HostInput` with mock
   probe results, `ScanHosts` returns correct `icmpReachable`/`OpenPorts`;
   `HostnameHint` takes precedence over reverse DNS when non-empty.
3. **Port-table coherence test**: assert `defaultPortList()` and all ports in
   `defaultPorts` match; assert `suggestForPort` produces a suggestion for every
   port in `defaultPortList()` (prevents silent drift).
4. **Freebox job integration test** (testcontainers): run the rewritten Freebox
   job against a stub Freebox HTTP server returning a two-host LAN list; after
   the job completes, both hosts have non-empty `open_ports` and
   `suggested_checks` in the DB (requires at least one open TCP port on the
   testcontainer host to confirm the scanner ran).
5. **No LAN scan regression**: existing `TestNetworkDiscovery*` tests pass
   unchanged.

## Implementation Plan

1. **Shared port table (`ports.go`)** — New file in `server/internal/discovery`
   defining `portSpec`, the authoritative `defaultPorts` slice (exactly the 16
   ports/types/URL templates from the spec), and `defaultPortList()` derived from
   it. Remove the old hardcoded `defaultPortList()` from `scanner.go`.
2. **Suggestion engine (`suggest.go`)** — Rewrite `suggestForPort` to look up the
   port in `defaultPorts` (range/match) and build the suggestion from the spec's
   `CheckType` + `URLTmpl`, producing identical output to today's switch.
3. **Explicit-host scan API (`scanner.go`)** — Add `HostInput{IP, HostnameHint}`,
   extract the shared worker-pool body of `Scan` into a private
   `runProbes(ctx, targets []probeTarget, ports, concurrency, timeout)`, and add
   `ScanHosts(ctx, hosts, cfg)` that resolves config defaults (ports, timeout,
   concurrency), ignores `cfg.CIDRs`, and calls `runProbes` with the pre-supplied
   targets. `probeTarget` carries `IP` + `HostnameHint`; `probeHost` prefers the
   hint over reverse DNS.
4. **Freebox job rewrite (`job_freebox_lan_discovery.go`)** — After
   `ListLanHostsForChannel`, build `[]discovery.HostInput` (IP + device name as
   hint), call `discovery.ScanHosts`, index results by IP, and merge: responsive
   hosts get real `icmpReachable`/`open_ports`/`suggested_checks`; hosts not seen
   by the scan fall back to the Freebox `reachable` flag with empty
   ports/checks (so no Freebox host is dropped). Persist via the existing upsert
   (no migration).
5. **Tests** — `ScanHosts` unit test (hostname-hint precedence, results shape);
   port-table coherence test (`defaultPorts`↔`defaultPortList()`↔`suggestForPort`);
   update the Freebox job test assertions for the active-scan merge while keeping
   the 2-host / idempotency / not-granted cases green.
6. **QA** — `make build-backend build-dash0 lint-back test` until clean.
