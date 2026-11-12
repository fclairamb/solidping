---
model: sonnet
effort: medium
---

# A dual-stack probe that only got through on its fallback family should say so

GitHub Issue: https://github.com/fclairamb/solidping-business/issues/69

## Source

Florent, 2026-11-11, prod, region paris (node eu2): every check targeting a dual-stack
k8xp master host timed out 7 to 9 times an hour from paris only. The IPv6 path from
that node to the target dropped packets while IPv4 was clean. The guard dialed the
pinned IPs one after the other, IPv6 first, each with the 30 s dial timeout, so under a
6 s probe budget IPv4 was never tried. The issue asked for three things:

1. Race v6 and v4 Happy-Eyeballs style. **Done** in `0c5ccaf3` (PR fclairamb/solidping#849,
   merged into the stacked batch branch, not yet on `main`):
   `server/internal/egress/happyeyeballs.go` starts the other family after 300 ms or on
   the first failure.
2. Report which family answered, and flag the probe as degraded, not down, when only one
   family of a dual-stack target fails. This spec covers it.
3. Optionally, two sub-probes (v4, v6) per dual-stack target. Kept as an open question.

## Problem

- The answering family is already reported: `connFamilyTracker`
  (`server/internal/checkers/checkhttp/checker.go:204`, `:1071`) stamps
  `output.ip_version` (`checkerdef.OutputKeyIPVersion`,
  `server/internal/checkers/checkerdef/ipversion.go:59`) from the connection that was
  established. Part of item 2 is therefore already there.
- What is missing: since the Happy Eyeballs fix, a target whose IPv6 path is broken shows
  **up** with `ip_version: ipv4` and nothing else. The operator loses the "IPv6 unreachable
  from paris" signal that the timeouts used to give. Neither
  `dialHappyEyeballs` (egress) nor the stdlib dialer (non-enforcing, self-hosted path)
  tells the checker that the preferred family failed or hung first.
- `StatusWarning` (`server/internal/checkers/checkerdef/types.go:80`) is the existing
  "up, but something to report" status: it counts as up for availability, is neutral for
  incidents, and the aggregation job rolls it up to `degraded`. That is the status the
  issue asks for.

## Why a spec

- A product decision the reporter didn't settle: when a fallback counts as a warning, and
  whether sub-probes exist at all.
- A new result output field and a new warning, shown in the dashboard and on status pages.
- It touches the HTTP checker, its trace, the dashboard result view and the docs: more than
  4 files.

## Proposal

1. **Observe the attempts.** Extend `connFamilyTracker`'s `httptrace.ClientTrace` (it
   already sees `ConnectStart` / `ConnectDone` per attempt, for both the guard's
   `net.Dialer` and the stdlib one) to record, per family: first attempt start time, and
   the outcome (connected, failed with error, canceled because the other family won).
2. **Detect a fallback.** A probe *fell back* when the established connection's family is
   not the family of the first `ConnectStart`, and the first family's attempt either failed
   with a non-cancel error, or was still pending after the fallback threshold (see open
   question 1) when the other family connected.
3. **Report it.** On a fallback, the HTTP check (and every checker using
   `checkerdef.HTTPTransportFor`: checkprometheus, …) returns `StatusWarning` instead of
   `StatusUp`, with:
   - `output.ip_fallback = {"failed": "ipv6", "reason": "<timeout|error text>", "after_ms": N}`;
   - a human message: `IPv6 unreachable from <region>, answered over IPv4`.
   A probe that is down for another reason (status code, assertion, TLS) keeps its status:
   the fallback info is still stamped in output, it never upgrades a failure.
   A check with `ipVersion` pinned never falls back, so never gets this warning.
4. **Dashboard.** Show `ip_fallback` in the result detail next to `ip_version`
   (`data-testid="result-ip-fallback"`).
5. **Tests.** Go: a fake dual-stack listener where the v6 address blackholes SYNs (as in the
   existing `egress_test.go` dual-stack test, `dual.acme.com`) asserts `StatusWarning` and
   the `ip_fallback` output; a clean dual-stack target stays `StatusUp`; a pinned family
   never warns. Cover both the enforcing (guard) and non-enforcing (stdlib) paths.
6. **Docs.** Document the warning and the output field where the check's output keys and
   `ipVersion` are documented.

## Open questions

1. **When does a fallback count as a warning?**
   Recommended: only when the first family *failed* (error or timeout) or was still pending
   **1 s** after its start when the other family connected. Trade-off: the 300 ms race
   delay alone would warn on any IPv6 path that is merely slow, which is noisy; 1 s catches
   the SYN-retransmit case from the incident (first retransmit at 1 s) without flagging
   ordinary latency.
2. **Two sub-probes (v4 and v6) per dual-stack target?**
   Recommended: no, not in this spec. Operators who want a per-stack status can already
   create a second check with `ipVersion: ipv6` (spec 2026-08-09-02). Trade-off: no
   automatic per-stack status page row, but no doubling of probe volume and no new result
   model.

## Closing the issue

This spec closes fclairamb/solidping-business#69 (item 1 was fixed by `0c5ccaf3`; the issue
was closed when this spec was filed). The implementing PR body **must** carry
`Closes fclairamb/solidping-business#69`. When the spec is archived to `specs/done/`, verify
the issue is closed and, if not, close it by hand:

    gh issue close 69 --repo fclairamb/solidping-business --comment "Implemented by <PR or commit>; spec: <archived spec path>"

## Resolved open questions

1. (autopilot: recommended answer) Warn only when the first family failed or was still pending 1 s after its start when the other family connected.
2. (autopilot: recommended answer) No sub-probes in this spec; a second check with `ipVersion: ipv6` covers it.
