---
model: opus
effort: medium
---

# Bound the "agent result rejected" log throttle: no agent-chosen key

## Problem

Found by the security scan of 2026-11-23 (finding AV1). Severity: Medium (availability of the whole server, every tenant). Who can trigger it: the operator of any enrolled private agent. On SaaS that means any user, since anyone can create an org and enroll an agent. Private agents are untrusted.

#983 (14149919) added `rejectResult` (`server/internal/handlers/agentws/handler.go:1619-1633`). It logs every refused result frame through a `DeprecationThrottle` (`h.rejectWarn`, created at `handler.go:206`) keyed on `state.agent.UID + "|" + frame.JobUID + "|" + reason` (`handler.go:1623`).

- `frame.JobUID` comes from the agent. `validateResultFrame` (`handler.go:1655-1675`) only checks that it is non-empty, and the frame cap is 256 KiB (`maxFrameBytes`, `handler.go:71`). So one key can be about 256 KiB.
- The "status out of range" path (`handler.go:1554-1557`) refuses the frame before any DB lookup, so each frame costs the agent almost nothing.
- `DeprecationThrottle` (`server/internal/agents/deprecation.go`) keeps every key for two intervals: `DefaultDeprecationLogInterval` is one hour, so entries live two hours. Its doc says memory stays bounded "by the number of distinct agents". That holds for `legacyWarn`, which is keyed on the agent UID (`handler.go:625`). It no longer holds for `rejectWarn`.
- A key never seen before is always let through. So each new `jobUid` also writes one WARN line carrying the whole `jobUid`.
- `sweep` (`deprecation.go`) walks the whole map, holding the single mutex, on every key it lets through. The cost per frame grows with the map, so total CPU grows quadratically. Every agent connection's refusal path waits on that one lock.

Impact: one agent streaming refused result frames, each with a different large `jobUid`, grows server memory at roughly its upload bandwidth, and nothing frees it for two hours. It also floods the logs at the same rate. This leads to an OOM kill or a full log disk for every tenant on the instance. Before #983, a refused frame was only answered with an error frame.

Affected versions: none released. 14149919 is on main after v0.53.0, so the flaw would ship with v0.53.1 (release PR #984 is open). Fix it before that release, or in the release right after.

## Proposal

1. `server/internal/handlers/agentws/handler.go` `rejectResult`: key the throttle on `state.agent.UID + "|" + reason` only. `reason` is one of three fixed strings set by the server. Keep `job_uid` as a log field, truncated to a short prefix (e.g. 64 bytes) so one log line stays small.
2. `validateResultFrame`: refuse a `jobUid` that is not a well-formed job UID (job UIDs are server-minted UUIDs; parse it, or at least cap its length), before the job lookup at `handler.go:1564`. This also keeps the oversized string out of `GetCheckJobByUID`.
3. `server/internal/agents/deprecation.go`: make the throttle safe whatever key it gets. Cap the number of entries (a few thousand). Past the cap, refuse new keys or drop the oldest ones, and count the overflow so it shows in the next allowed line. Run `sweep` at most once per interval instead of on every allowed key. Update the type doc: memory is bounded by the cap, not by "distinct agents".

## Tests

- `server/internal/handlers/agentws/` (next to `result_frame_status_test.go`): an agent sends N result frames (e.g. 5 000) with distinct `jobUid`s and an invalid status. Assert the `rejectWarn` throttle holds at most one entry per (agent, reason), and that at most one WARN line is written per (agent, reason) per interval (capture the logger). This fails before the fix: N entries and N lines.
- Same file: a `jobUid` longer than the cap, or not a UID, is refused with `VALIDATION_ERROR` before any DB call (use a DB mock that fails the test if called).
- Positive control: a valid result frame for a job the agent owns is still recorded. The first refusal of each reason for an agent is still logged once, with its job UID prefix and the suppressed count on the next line.
- `server/internal/agents/deprecation_test.go`: with more distinct keys than the cap, the map never grows past the cap and the overflow is counted. Existing per-agent behaviour (one line per key per interval, suppressed count) is unchanged.
