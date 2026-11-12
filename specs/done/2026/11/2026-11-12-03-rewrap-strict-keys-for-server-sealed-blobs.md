---
model: opus
effort: high
---

# A peer re-wrap of a server-sealed blob can still inject public config keys

## Problem

Found by the security scan of 2026-11-12 (finding AK3).

**Severity: Medium (integrity).** This is a gap in the AJ2 fix of #840 (spec `2026-11-11-02`), which no release contains yet. Who can trigger it: the operator of an enrolled private agent of the org, whose key is a current recipient, while a re-wrap of one of the region's checks runs. Private agents are untrusted from the server's point of view.

The AJ2 fix refuses a re-wrapped plaintext that carries a key that is not a secret field of the check type: *"so it can never override a public field"*. It applies that rule only to browser-origin blobs:
- `OpenSealedPlaintext` returns a non-browser, unbound plaintext as is, with no `StrictSecretKeys` check (`server/internal/crypto/credentials/binding.go:241-243`).
- The re-wrap executor and the confirmation both go through it (`server/internal/rewraptask/executor.go:65`, `:89-97`, `:117-120`), so for a server-origin blob they check nothing.
- `RewrapSealedEnvelope` (`credentials/sealing.go:392`) keeps the old envelope's `origin: server` on the agent-produced blob.
- At claim, the agent merges that plaintext over the public config, and the secrets win (`checkworker/backend/ws.go:367`, `credentials.MergeConfig`).

Server-origin blobs do go through peer re-wrap:
- A check whose regions are all private keeps no server-side envelope (`server/internal/handlers/checks/service.go:5906`, `sealedOnly`).
- The server's own re-seal on enrollment only handles mixed-mode rows (`server/internal/handlers/agents/service.go:925-929`).
- So a recipient drift (a new agent, a region key set or rotated) flags such a check, and only a peer re-wrap clears it.

**Impact.**
- The picked agent answers its possession challenge honestly, since it is a recipient. It can then return a blob sealed to the current recipients whose plaintext adds public keys, for example `url`, `host` or `verifySsl`.
- The confirmation cannot catch it in two cases:
  - the verifier cannot compare old and new plaintexts, typically when it is the newly enrolled agent that caused the drift (`executor.go:139-145`);
  - there is no second agent, and the blob is stored unconfirmed (`verify.go`, `storeUnconfirmed`). The claim path still uses an unconfirmed blob.
- Every agent of the region then runs the check against the injected config. The malicious operator already holds the secrets, so this does not disclose them. What it gives them:
  - they can hide outages or fake alerts;
  - they can point the other agents' runs, with the org's secrets, at hosts inside those agents' networks. The server-side egress check never sees the merged config.

## Proposal

1. In the re-wrap executor and the confirmation, refuse any plaintext key that is not a secret field of the type, **whatever the blob's origin** (`rewraptask/executor.go:89-97`).
   - Call `StrictSecretKeys` directly, in addition to `OpenSealedPlaintext`.
   - A stored server blob is sealed from `SplitConfig`'s private half (`handlers/checks/service.go:5935`, `agents/service.go:961`), so it only ever holds secret fields.
2. Mark an agent-produced blob as such. `RewrapSealedEnvelope` writes `origin: agent` (or `browser`), not the old origin. `OpenSealedPlaintext` then applies the strict-keys rule at claim to every origin except `server`.
   - Do not apply it to the per-claim blob the server seals for a *system* agent: that one legitimately carries the resolved `${param:}` overlay of public fields (`server/internal/handlers/agentws/handler.go:1282-1300`), and it is never stored.
   - Alternatively, apply the strict rule at claim to every stored blob of an org agent. Choose the variant that keeps the system-agent path working, and document the choice in the code.
3. The Re-seal page and `rewrap` status keep reporting an unconfirmed server-origin re-wrap as *not confirmed* (`CodeNotConfirmed`), as they do for browser blobs.
4. Docs: say in `web/docs/docs/features/browser-sealed-secrets.md` (§ re-wrap) that the rule holds for server-sealed checks of private-only regions too.

## Tests

- **Regression** in `server/internal/rewraptask/executor_test.go` (fails on main today): the re-wrap executor fed a **server-origin** blob whose plaintext carries `url` next to `password` refuses with `CodeNotSecret`. The confirmation executor does the same on a pending server-origin blob.
- **Regression** at claim, in `checkworker/backend/ws_binding_test.go`: an agent-produced blob (origin `agent`) carrying a public key is refused. A blob of origin `server` with only secret fields is still accepted.
- **Positive controls:**
  - a server-origin blob with only secret fields re-wraps and is stored;
  - the system-agent per-claim blob that carries a `${param:}` overlay of a public field still runs (`agentws` claim test).
