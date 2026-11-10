---
model: opus
effort: high
pr: batch
---

# Sealed secrets: peer re-wrap, `solidping seal` and config-as-code

Phase 3 of [2026-11-08-01 browser-sealed check secrets](2026-11-08-01-browser-sealed-check-secrets.md). Phases 1 and 2 shipped: `sealedSecrets` on the check write API, `GET /orgs/:org/sealing-recipients`, the org setting `security.browser_sealing.mode`, the claim filter on sealed fingerprints, and shared region keys (`sealing.region_keys`, `SP_AGENT_REGION_KEYS`, `region-key-<fp>` claim capability).

## Problem
- A new agent in a `perAgent` region, or a region key rotation, flags every sealed check `needsReseal`, and today the only fix is typing each check's secrets again.
- Sealing from a script needs the `age` CLI plus a manual recipients fetch.
- Generating a region key offline is not possible: only dash0 generates one.
- A config-as-code file cannot carry sealed secrets: import keeps the stored ones (v1).

## Proposal

### 1. Peer re-wrap
- An admin clicks "Re-seal for the current agents" on the region page (and on the needs-re-seal alert of a check).
- The server sends a `rewrap` worker task to one online agent that is a recipient of the blobs (its X25519 fingerprint, or a region key fingerprint it advertises, is in their `fingerprints`).
- The task carries, per check: the sealed blob and the new recipient list (the current `RecipientsForRegions`). The agent opens each blob with `UnsealWithIdentities`, seals it to the new list with `SealForRecipients`, and returns only the new blob.
- The server checks the returned blob like a `sealedSecrets` write (header stanza count, fingerprints equal the current recipients, size), keeps the `origin`, stores it on the check and its jobs, and clears `needsReseal`.
- The server only relays ciphertext. The recipient list comes from the server, the same trust the browser already gives it.
- Failure path: no online recipient agent, the task times out, or a returned blob fails validation. The check keeps its old blob and its flag, and the page says why.

### 2. `solidping seal`
`solidping seal --server … --org … --region @dc1 < secrets.json` fetches the recipients with a PAT (`SP_TOKEN`), seals the JSON object, and prints the `sealedSecrets` object. Same binary, `credentials.SealForRecipients`.

### 3. `solidping agent region-key generate`
Prints a fresh age X25519 identity and its recipient and fingerprint, offline, for the operator to paste the recipient in dash0 (the paranoid path of spec 2026-11-08-01 §2). dash0 gets a "Paste a public key" option next to "Use a shared region key".

### 4. `sealedSecrets` in config-as-code
Export keeps stripping secrets. Import and apply accept a `sealedSecrets` object per check, validated exactly like the write API, including `409 SEALING_RECIPIENTS_CHANGED` as an item error. The dry run validates it without storing.

## Tests
- Go, agent: a `rewrap` task re-seals a blob to a new recipient list, and the old identity no longer opens it; a blob the agent cannot open is reported, not dropped.
- Go, server: a re-wrap result with a wrong stanza count or stale fingerprints is refused; a valid one clears `needsReseal`; no online recipient gives a clear error.
- Go, CLI: `solidping seal` output is accepted by the write API; `region-key generate` prints a recipient whose fingerprint matches `RecipientFingerprint`.
- Go, import: a document with `sealedSecrets` stores a browser-origin blob; stale fingerprints are an item error in the dry run and the real run.
- dash0 Playwright: re-seal from the region page after a key rotation clears the flag.

## To verify
- The worker task framework (spec 2026-10-22-02) can carry a payload of a few hundred KiB (one blob per check, up to 64 KiB each), or the task must be chunked.
