---
model: opus
effort: high
---

# A peer re-wrap is handed to an agent on its own word, and its result is stored unchecked

## Problem

Found by the security scan of 2026-11-11 (finding AJ2).

- **Severity:** Medium (integrity: no secret is disclosed).
- **Who can trigger it:** the operator of an enrolled private agent of the org. On the server's side that is an
  untrusted party. It also needs an org admin to start a re-seal (`POST /orgs/:org/sealing/rewrap`) while some
  checks are flagged, for example right after a region key rotation.

Peer re-wrap (#835, `server/internal/rewraptask/server.go`) asks an agent that can open a check's blob to re-seal it
for the current recipients. Three gaps chain:

1. **Possession is never proven.** `pickAgent` / `agentCanOpen` (`server.go:355-423`) pick the most recently seen
   live agent whose key is in the blob. That key is either:
   - the agent's `X25519PublicKey`, which the agent itself sent at enrollment
     (`server/internal/handlers/agentws/handler.go:320` only checks the `age1` prefix);
   - or a `region-key-<fp>` capability it advertises (`server/internal/sealing/sealing.go:250-260`).

   Both are public values (`GET /orgs/:org/sealing-recipients`). An agent can register a copy of another
   recipient's public key, or advertise a fingerprint it does not hold, and be picked. `prepareClaim`
   (`server.go:117-140`) then hands it the envelope and the current recipients.
2. **The result is not checked.** `validateResult` (`server.go:186-212`) only compares the fingerprint list the
   agent *reports* with the current recipients, and counts the age stanzas (`credentials.ValidateSealedCiphertext`,
   `server/internal/crypto/credentials/sealing.go:233-236`: "a sanity check, not proof of the recipients").
   `store` (`server.go:216-250`) then writes the blob on the check and all its jobs, and `needsReseal` clears.
   Anyone can seal a new age file to the current public keys, so the agent can return any plaintext.
3. **The plaintext can override public config.** On every agent, the unsealed map is merged over the job config
   with the private side winning (`credentials.MergeConfig`, `server/internal/crypto/credentials/secret_fields.go:61`;
   `server/internal/checkworker/backend/ws.go:340-355`). Nothing restricts it to the type's secret fields.

So a member-run agent that never could open the blob can replace it with `{"url": …}` or other config of its
choice. The check then runs on the org's *honest* agents against that config: an outage can be hidden behind a
host that always answers 200, or false alerts produced. The flag clears, the Re-seal page shows the check as done,
and the blob keeps its `origin: browser` (`RewrapSealedEnvelope`, `sealing.go:334`). The original secrets are not
disclosed (they are just lost), which is why this is Medium and not High. GLM `sql` and `auth` rated the
"keep access after a rotation" angle High. That angle gives an agent that already held the key nothing new, so it
does not count on its own.

## Proposal

1. **Prove possession before the hand-over.** In `prepareClaim`, seal a random 32-byte challenge to the key the
   agent was picked for (its enrolled key, or the region key), and nothing else. The executor
   (`rewraptask/executor.go`) opens the challenge with its identities and returns it in `Result` next to the new
   ciphertext. `complete` refuses the result (`CodeInvalidResult`, check left as it was) unless the challenge
   matches. Keep the challenge only on the server (task payload), never in a response to anyone else.
2. **Only secret fields in a blob.** Agent side, after unsealing (`ws.go` claim, tunnel unseal, rewrap executor),
   refuse a plaintext with any key that is not a secret field of the check type. Report it as an explicit error
   result, never merge it. Make `MergeConfig` callers for sealed blobs go through a filtered variant. This step is
   shared with spec `2026-11-11-01` (AJ1). Whichever lands first does it, and the other references it.
3. **Confirm with a second recipient when there is one.** Before swapping, send a `verify` task to another online
   agent holding a current recipient key. It only opens the pending blob and answers ok. Store on ok. With no
   second holder online, store it but keep `needsReseal` with the reason "not confirmed by a second agent".
4. **Refuse a duplicate agent key at enrollment.** Refuse an `X25519PublicKey` equal to another live agent's key or
   to a stored region key of the org (`agentws/handler.go:320`). That is never legitimate, and it is how gap 1 is
   reached in per-agent mode.
5. Docs (`web/docs/docs/features/browser-sealed-secrets.md`, "Re-sealing on an agent"): describe the possession
   challenge and the second-agent check.

## Tests

- Go, `server/internal/rewraptask/server_test.go`:
  - a picked agent that returns a valid-looking result without the right challenge → refused, the old blob is
    kept, the check stays flagged. Positive control: an agent holding the key → stored, flag cleared;
  - a result whose blob a second online holder cannot open → not stored, flagged with the reason;
  - no second holder online → stored, `needsReseal` kept with the "not confirmed" reason.
- Go, `server/internal/checkworker/backend` (ws claim): a blob whose plaintext holds `url` (not a secret field of
  `http`) → error result, the job's public `url` is used nowhere. Positive control: a blob with only
  `basicAuth` → merged and run.
- Go, `server/internal/handlers/agentws`: enrolling with another live agent's public key, or with the region key's
  recipient → refused. A fresh key → accepted.
