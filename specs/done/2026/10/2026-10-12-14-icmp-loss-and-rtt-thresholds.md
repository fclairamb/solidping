---
model: sonnet   # haiku | sonnet | opus
effort: medium  # low | medium | high
---

# An ICMP check stays up with 80% packet loss or a 2 s round trip

## Problem
An ICMP check is up as soon as one reply comes back (`server/internal/checkers/checkicmp/checker.go:224-228`). The comment at `:289-290` makes it explicit: partial loss is treated as degraded reachability, not a failure. A line dropping 4 packets out of 5, or answering in 2 s, is green.

The data to judge it is already computed for every burst:
- `packets_sent`, `packets_received`, `packet_loss_pct` (`checker.go:217-222`, metrics at `:236-240`);
- `rtt_ms_min`, `rtt_ms_max`, `rtt_ms_avg`, `rtt_ms_jitter` over the replies (`:249-274`);
- `result.Duration` is the average RTT when at least one reply came back (`:257`).

There is no knob to act on it. `ICMPConfig` has `count`, `interval`, `timeout`, `packet_size`, `ttl` only (`checkicmp/config/config.go:18-27`). `count` is 1-600, default 1, `interval` 10ms-60s, default 1s (`config/limits.go:9-15`, rules in `config/validate.go:28-37`).

How other checks express thresholds, for consistency:
- `StatusWarning` is "up but something to report": amber, counts as up, no incident (`checkerdef/types.go:71-76`). Prometheus (`checkprometheus/config/config.go:110-142`), RabbitMQ, domain and SSL use it for a warning tier and `StatusDown` for the critical tier.
- Degraded detection (`wiki/features/degraded-detection.md`, `server/internal/degraded/rule.go`) already has a soft latency signal. The check-level `slow_threshold_ms` fires a notify-only `degraded` incident when M of the last N up probes exceed it. It reads `result.Duration`, which for ICMP is the average RTT, so it already works for ping.

## Proposal
1. Config (`checkicmp/config/config.go:18-27`): add two optional pointer fields, snake_case like `packet_size`:
   - `MaxPacketLossPercent *float64`, JSON key `max_packet_loss_percent`;
   - `MaxAvgRTTMs *float64`, JSON key `max_avg_rtt_ms`.
   Both `omitempty`, with the same `tagliatelle` nolint as `packet_size`.
   Pointers because `0` is meaningful for loss ("any loss is down"), same reasoning as Prometheus `warningValue`. `FromMap` accepts int and float64 like `count` (`:66-73`) and rejects other types. `GetConfig` (`:111-141`) writes them only when non-nil.
2. Validation (`checkicmp/config/validate.go`):
   - `max_packet_loss_percent` between 0 and 100 inclusive.
   - `max_avg_rtt_ms` greater than 0 and at most 30000 (the timeout cap at `:49`).
   - No rule tying either to `count`. See open questions.
3. Checker (`checkicmp/checker.go`):
   - `checker.go` is being edited concurrently by the `packet_size`/`ttl` fix, so line numbers here may drift. Anchor on the comments `Calculate statistics` and `TOTAL loss only`.
   - Extract the stats and status block (`:185-295`) into a pure `summarizeBurst(cfg *ICMPConfig, results []pingResult) *checkerdef.Result`, so thresholds are testable without raw sockets (`TestICMPChecker_Execute` needs the network, `checker_test.go:406-412`). Do not touch `burst.go`, another spec is changing it.
   - Thresholds only apply when at least one reply came back. Total loss stays exactly as today: down, `no successful ping responses`, network failure set (`:275-293`).
   - Loss: `packet_loss_pct > max` gives `StatusDown` with `packet loss 40% > 20%`.
   - RTT: `rtt_ms_avg > max` gives `StatusDown` with `avg rtt 180 ms > 100 ms`.
   - Strictly greater, so a value equal to the limit is up. Both breached: both messages joined with `; `.
   - The message goes in `Output["error"]`, which incidents use as `failure_reason` (`handlers/incidents/failure_details.go:123-128`). No `SetNetworkFailure` call: the target answered.
   - Metrics and `Duration` are unchanged.
   - Numbers print with at most one decimal (`33.3%`, `0.4 ms`).
4. Dash0 form (`web/dash0/src/components/checks/form/types/network.tsx`, `icmpModule` at `:808`):
   - Add `maxPacketLossPercent` and `maxAvgRttMs` to the state and to `ownedKeys` (`:815`), so clearing an input deletes the key.
   - Two inputs in the burst section after TTL: `data-testid="check-icmp-max-loss-input"` and `check-icmp-max-avg-rtt-input`. Same parsing style as `packet_size`. Shape only, bounds stay on the server.
   - Hint `data-testid="check-icmp-loss-count-hint"` when a loss limit is set and `count` is empty or 1: "With 1 packet, loss is either 0% or 100%. Send at least 10 packets for a useful percentage."
   - Locale keys in all four `web/dash0/src/locales/*/checks.json` (next to `network.burstPacketSize`, en `:1251`).
5. Docs (`web/docs/docs/features/check-types.md:320-345`): two rows in the ICMP table and one sentence on how they turn the check down. While there, fix the stale defaults: the table says Count `3`, the code says 1 (`limits.go:10`).

## Tests
- `server/internal/checkers/checkicmp/checker_test.go`, `TestICMPConfig_FromMap` (`:26`) and `TestICMPConfig_GetConfig` (`:186`): both fields parse from int and float64, round-trip, are absent when unset, and a string is rejected.
- Same file, `TestICMPChecker_Validate` (`:225`): loss 0 and 100 accepted, -1 and 101 rejected. RTT 1 and 30000 accepted, 0, -5 and 30001 rejected.
- New `checker_summary_test.go` on `summarizeBurst`, table-driven:
  - no thresholds, 1 reply out of 5: up (current behaviour kept);
  - loss 40% with max 20: down, error `packet loss 40% > 20%`;
  - loss 20% with max 20: up (boundary);
  - loss 0% with max 0: up. One lost packet out of 10 with max 0: down;
  - avg 180 ms with max 100: down, error `avg rtt 180 ms > 100 ms`. Avg 90 ms: up;
  - both breached: both messages;
  - total loss with both thresholds set: down with `no successful ping responses` and a network failure, no threshold message.
- `web/dash0/src/components/checks/form/types/network.test.ts`: `fromConfig`/`toConfig` round-trip for both keys, an empty input omits the key, `0` loss is kept (not dropped as falsy).
- `web/dash0/e2e/check-icmp-burst.spec.ts`: create with count 10, loss 20 and RTT 150, reopen the edit form, values persist. Clearing the loss input deletes the stored key (next to `:154`). The count hint shows with count 1 and a loss limit, and is hidden with count 10.

## To verify
- The ICMP docs row calls `timeout` a "Total timeout" with default `10s`. The code default is 5s (`limits.go:9`), the worker threads its own budget when unset, and the config comment treats it as per packet. Check which one is true before correcting that row.

## Resolved open questions

**Decision (user, 2026-10-12): adopt the recommended answer of every question below. Implement exactly those recommendations.**
- RTT over the limit: `down` or `warning`? Recommended: `down`. The field is an explicit hard limit, and the confirmation period already absorbs a single spike. The soft signal already exists: `slow_threshold_ms` degraded detection works on ICMP today because `Duration` is the average RTT. A warning tier (`warn_avg_rtt_ms`, like Prometheus `warningValue`) can come later if asked.
- Reject or warn when a loss limit is set with `count` 1? Recommended: neither on the server. It is coarse but not wrong (it behaves like today). The form hint and the docs are enough.
