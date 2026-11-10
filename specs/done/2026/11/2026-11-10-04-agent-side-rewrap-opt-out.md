---
model: opus
effort: medium
pr: batch
---

# Sealed secrets: let the agent's operator turn the peer re-wrap off

Follow-up of [2026-11-10-03 peer re-wrap](2026-11-10-03-sealed-secrets-peer-rewrap-and-cli.md), asked by Florent on 2026-11-10.

## Problem
- Every agent registers the `rewrap` executor unconditionally. It opens a sealed blob and seals it to **whatever recipient list the server sends**.
- So a compromised server can add its own key to the list, start a re-wrap and read the secrets. The spec accepts this ("the same trust the browser already gives it"), but an org that wants the full "the server never sees our passwords" guarantee has no way to refuse it.
- An org setting would not help: a compromised server flips it back. Only the agent's operator can make the refusal stick, as for LAN discovery (`SP_AGENT_DISCOVERY_ENABLED`).
- The agent logs only the recipient count, so an operator cannot spot an unknown key afterwards.

## Proposal
1. **`SP_AGENT_REWRAP_ENABLED`** (default `true`, YAML `agent.rewrap_disabled`). With `false`, the agent does not register the executor and does not report the `rewrap` capability. The server then never targets it (`agentCanOpen` already requires the capability).
2. **Clear reason on the server.** When an online agent holds a key the blob was sealed to but none runs the re-wrap, the outcome is `rewrap_not_offered` (not `no_online_recipient`, which tells the admin to start an agent). Covers both an older agent and one with the opt-out. dash0 shows "the agents that hold these secrets do not run the re-seal (older than 0.47, or `SP_AGENT_REWRAP_ENABLED=false`): type the secrets again".
3. **Audit log on the agent.** "Sealed secrets re-wrapped" logs the sorted fingerprints it sealed to, and the startup log says whether the re-wrap is on.
4. Default stays on: the re-wrap is what makes `perAgent` regions and key rotation bearable. Docs say plainly that the full guarantee needs `false` on every agent of the region.

## Tests
- Config: `SP_AGENT_REWRAP_ENABLED=false` sets the opt-out; absent or unparseable leaves the re-wrap on.
- Server: an online key holder without the capability gives `rewrap_not_offered`; no key holder at all still gives `no_online_recipient`.

## Docs
`private-locations.md` env table, `browser-sealed-secrets.md` (Re-sealing on an agent), `wiki/features/deported-agents.md`.
