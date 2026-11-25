---
model: sonnet
effort: medium
pr: batch
automerge: false
---

# No written rule for how long the server supports old agents

## Problem

Agents are run by customers and lag behind the server (prod, 2026-11-26: 0.49.3 against
0.54.1). Compatibility shims for old agents pile up (`ClientFrame.PredatesAutoUpgrade()`,
three-state `Capabilities`, legacy check types from spec 2026-11-26-02) with no rule for when
they may go. Customers have no signal that their agent is about to stop working either.

## Proposal

1. **Rule (Florent, 2026-11-26).** The server stays compatible with any agent version released
   at least **1 month** ago, as a hard minimum. Aim for **6 months** whenever keeping a shim
   costs little code. A compatibility shim may be removed only once the agent versions that need
   it were all released more than 1 month ago. The server never refuses an agent connection
   because of its version, except on a protocol break (`ProtocolVersion`,
   `server/internal/agents/protocol.go:14`).
2. **`MinSupportedAgentVersion`** constant next to `ProtocolVersion`. Raised only when a shim is
   removed. A unit test reads the release dates from the root `CHANGELOG.md` and fails when the
   release that introduced `MinSupportedAgentVersion`'s replacement behaviour, i.e. the first
   version >= it, is less than 30 days older than the newest release in the changelog.
   Start it at the oldest version the server still fully supports today (to verify, likely 0.49.0).
3. **Unsupported badge.** An agent reporting a version below `MinSupportedAgentVersion` gets a
   red "Unsupported" badge instead of the amber "Drifted" one (`AgentVersionCell`,
   `web/dash0/src/components/shared/agent-version.tsx`, both the org page and
   `server.agents.tsx`), test id `<cell>-unsupported`, fr "Non supportée". Expose
   `versionSupported: false` on the agent lists. The server logs one WARN per connection.
   The connection is still accepted.
4. Write the rule in `wiki/conventions/agent-compatibility.md` (indexed in `wiki/README.md`) and
   add a one-line pointer in `server/AGENTS.md`: "Before removing an old-agent shim, check
   wiki/conventions/agent-compatibility.md".
5. When spec 2026-11-26-03 is in, its email and banner say "unsupported" for such agents.

## Tests

- Unit test of the changelog-date rule: passes on the current tree; fails with a fake changelog
  where the minimum version is 10 days old (negative case).
- agentws handler test: an agent below the minimum connects, gets `versionSupported: false`,
  one WARN, and still claims jobs.
- `agent-version.test.tsx`: unsupported badge rendered below the minimum, drifted badge above it,
  "unknown" when the version is null (never unsupported).

## To verify

- The `CHANGELOG.md` heading format carries a date for every release (release-please writes
  `## [x.y.z](...) (YYYY-MM-DD)`).
- The oldest agent version the current server still serves correctly, to seed the constant.
