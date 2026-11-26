# Agent compatibility window

Private-location agents are run by customers and lag behind the server (prod, 2026-11-26: 0.49.3 against 0.54.1). Compatibility shims for old agents (`ClientFrame.PredatesAutoUpgrade()`, the three-state `Capabilities`, the frozen `legacyCheckTypes` list) need a rule for when they may go.

## The rule

- The server stays compatible with any agent version released at least **1 month** ago: a hard minimum.
- Aim for **6 months** whenever keeping a shim costs little code.
- A shim may be removed only once every agent version that needs it was released more than 1 month ago.
- The server never refuses an agent connection because of its version. The only exception is a protocol break (`ProtocolVersion`, `server/internal/agents/protocol.go`).

## `MinSupportedAgentVersion`

Constant next to `ProtocolVersion`. Raise it only when a shim is removed. `TestMinSupportedAgentVersionRespectsCompatWindow` reads release dates from the root `CHANGELOG.md` (`## [x.y.z](...) (YYYY-MM-DD)`) and fails when the first release at or above the constant is less than 30 days older than the newest release. It was seeded at 0.41.0, the oldest release comfortably outside the window.

## What an unsupported agent sees

An agent reporting a version below the constant still connects and claims jobs. The server logs one WARN per connection ("agent version is no longer supported"), the agent lists expose `versionSupported: false`, and the dashboard shows a red "Unsupported" badge (`AgentVersionCell`, test id `<cell>-unsupported`) instead of the amber "Drifted" one. An agent with no reported version is never unsupported.
