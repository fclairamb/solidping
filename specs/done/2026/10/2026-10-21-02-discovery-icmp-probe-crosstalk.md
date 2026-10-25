---
model: sonnet
effort: medium
---

# Discovery ICMP probes read each other's replies, and never try the unprivileged socket

Part of the agent-run work series (see 2026-10-21-01). Independent of the others, but
2026-10-22-04 relies on it: on an agent, the raw socket also sees replies to the agent's own `icmp` checks.

## Problem
`runProbes` (`server/internal/discovery/scanner.go:159-167`) opens one raw `ip4:icmp` socket
shared by up to `concurrency` goroutines (default 64). `pingHost` (`scanner.go:290-334`) sends
every probe with `ID: 1, Seq: 1`, then returns true on the first EchoReply it reads, without
checking the source address, ID or Seq. So:
- false positives: host A is marked reachable by host B's reply, or by any unrelated echo reply
  the host receives (on an agent: replies to its own `icmp` checks);
- false negatives: A's real reply is consumed by another goroutine's `ReadFrom`.

It also only tries the raw socket, which needs `CAP_NET_RAW`. The image runs distroless nonroot
(`Dockerfile:141`), so containers fall back to TCP-only with one WARN (`scanner.go:161-164`).
Hosts that answer ping but none of the scanned ports disappear. `checkicmp` and `nettrace` already
try the unprivileged `udp4` datagram socket first (`server/internal/checkers/checkicmp/burst.go:583-605`,
`server/internal/nettrace/prober_icmp.go:41-50`).

## Proposal
1. In `scanner.go`, replace the per-goroutine read with one reader goroutine per scan that
   demultiplexes replies to waiting probes through a map keyed by `(source IP, seq)`:
   - each probe gets a unique `Seq` (counter per scan); `ID` is per scan (raw socket) and ignored
     on match for `udp4`, where the kernel rewrites it;
   - a reply is delivered only if its source IP and seq match a pending probe; anything else is
     dropped;
   - the probe waits on its channel with the existing per-probe timeout.
2. Socket choice: try `udp4` first, then raw `ip4:icmp`, the same order as checkicmp. Destination
   is `*net.UDPAddr` for `udp4` and `*net.IPAddr` for raw. Reuse checkicmp's helper if it is
   exported or trivially extractable; otherwise copy the 20 lines with a comment pointing at it.
3. Surface whether ICMP was used: add `ICMPAvailable bool` to the scan result (a new return value
   or a small result struct from `Scan` / `ScanHosts`, `scanner.go:105-135`). Spec 2026-10-22-04 reports it
   to the dashboard. Keep the WARN log.
4. Put the reader behind a minimal interface (`ReadFrom` / `WriteTo` / deadlines) so tests can
   inject a fake connection.

## Tests
- `server/internal/discovery/scanner_icmp_test.go` with a fake packet conn:
  - positive control: a reply from A with A's seq marks A reachable;
  - a reply from B never marks A reachable;
  - a reply from A with a wrong seq is ignored;
  - an unrelated echo reply (no pending probe) is dropped, nothing panics;
  - 64 concurrent probes with replies delivered in random order: each host gets exactly its own
    result (no cross-talk), run with `-race`.
- Socket selection: when `udp4` fails and raw succeeds, raw is used; when both fail, the scan is
  TCP-only and `ICMPAvailable` is false.
- Existing `scanner_test.go` and `scanner_egress_test.go` still pass.

## To verify
- Whether `checkicmp`'s socket helper (`burst.go:583-605`) is exported or in a shared package.
