---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# ICMP checks get placed on regions whose workers cannot open an ICMP socket

## Problem
Since 2026-10-25 the dev check `default/icmp` (regions `tokyo`, `gravelines`) fails from
Tokyo on every run with `listen ip4:icmp 0.0.0.0: socket: operation not permitted`
(7000+ failures, the incident flaps RECOVERED / REOPENED on the alert topic).

Tokyo is staffed by a Fly agent (`solidping-agent-nrt-dev`; prod twin `solidping-agent-nrt`):
the distroless `nonroot` image on a Firecracker VM. `listenICMP`
(`server/internal/checkers/checkicmp/burst.go:589`) tries the unprivileged `udp4` socket,
refused because the VM kernel keeps the default `net.ipv4.ping_group_range = "1 0"`, then the
raw `ip4:icmp` socket, refused without `CAP_NET_RAW`. Not a regression: ICMP never worked from
Fly. Nothing tells the user: Tokyo is offered for ICMP checks, and the failure reads as the
target being DOWN. Prod has no Tokyo ICMP check today, but any user can create one.

## Proposal
1. Add `CapabilityICMP = "icmp"` next to `CapabilityBrowser` in
   `server/internal/db/models/worker.go:80`, and to `regions.aggregatedCapabilities`
   (`server/internal/regions/capabilities.go:91`).
2. Probe it in `checkicmp`: an exported `ICMPAvailable()` that opens and closes the same
   sockets `listenICMP` uses (udp4, then ip4:icmp; IPv6 likewise, either family counts).
   Cache the verdict with a TTL like `checkbrowser/availability.go`.
3. Fold it into the reported set like `egressreport.WithBrowser`
   (`server/internal/checkworker/egressreport/egressreport.go:79`), same nil-base rule, for
   both the in-process worker and the agent.
4. `requiredCapabilities` (`server/internal/handlers/checks/placement.go:221`): require
   `icmp` for `type=icmp`, like browser. Same advisory semantics as browser (creation-time
   warning / region picker), not scheduling.
5. dash0 region picker: show "ICMP unavailable" for a region whose verdict is `no`, as it
   does for browser.
6. A socket-open EPERM at check time becomes an explicit infrastructure error (message says
   the worker cannot send ICMP: `ping_group_range` / `CAP_NET_RAW`), not a plain target DOWN.
7. Docs: the Fly agent page (`deploy/fly/`, `web/docs`) states a Fly agent cannot run ICMP
   checks and why.

Do NOT add file capabilities (`setcap cap_net_raw`) to the binary: `execve` then fails with
EPERM wherever NET_RAW is dropped from the bounding set, so the server would not start at all.

## Tests
- `checkicmp`: availability probe returns false when both listens fail (inject the listen
  func), true when either succeeds.
- `egressreport`: `icmp` is added only to a non-nil base; nil stays nil.
- `regions`: aggregate gives `no` for a region whose live workers report a set without
  `icmp`, `unknown` for a NULL set, `yes` if any live worker has it.
- `handlers/checks` placement: an icmp check on an `icmp=no` region gets the warning; an http
  check on the same region does not (negative case).
- dash0 Playwright: region picker shows the ICMP-unavailable marker (`data-testid`).

## To verify
- `worker.go:68` says "SQLite triggers reject anything else": a new capability name likely
  needs a new migration (next `NNN`, never edit a tagged file) for both dialects.
- Exact names/locations of the browser availability cache and the dash0 picker component.
