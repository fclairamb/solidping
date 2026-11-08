---
model: opus
effort: high
pr: own
---

# Browser-sealed check secrets for private agents

> Feasibility study. Florent reviews the design and answers the open questions before anything is implemented.

## Problem
An org that runs checks on its own private agents still types its check passwords into dash0, and the SolidPing server receives them in plaintext.

For a check that targets only private regions, the server already seals the secrets to the agents' X25519 keys and keeps no decryptable copy (`applyRegionSealing`, `server/internal/handlers/checks/service.go:5794`). But the plaintext still crosses the server at write time: request body, process memory, any request logging, a debugger, a compromised process. A SaaS customer has to trust the operator for that window.

We want an option where the password is encrypted in the browser, for keys only the org's agents hold, so the server never receives it. Marketing line: "Your private agents, your private keys, your private passwords."

## Verdict
Feasible, and smaller than it looks. Most of the machinery ships already:

- Each agent generates an age X25519 keypair locally and registers only the public key (`server/internal/agentmode/agentmode.go:67-73`, `agents.x25519_public_key`). The private key stays in `SP_AGENT_KEYS_FILE` (mode 0600) or `SP_AGENT_KEYS`.
- Sealed-only checks store an age envelope in `checks.config_sealed` and `check_jobs.config_sealed` (`v: 2`, `alg: "age-x25519"`, sorted recipient `fingerprints`, `ct` = base64 of the age binary, `server/internal/crypto/credentials/sealing.go:24-46`). `config_private` is null for them.
- Org agents get `configSealed` verbatim and open it themselves (`ToAgentJob`, `server/internal/agents/protocol.go:266-293`; `UnsealWithIdentity`, `server/internal/checkworker/backend/ws.go:318-334`). The server never decrypts it.
- Shared workers can never claim a private region: three independent locks (`ValidateWorkerRegion`, `MatchesRegion`, a DB check constraint on `workers.region`).
- GET never returns secrets, only `configPrivateKeys` and `needsReseal`. dash0 already treats secrets as write-only (`<Input type="password">`, no reveal, the "Encrypted, enter new values" hint).

The missing piece is the write path: produce the same age envelope in the browser instead of on the server. The agent does not change. `wiki/features/deported-agents.md:611` lists browser sealing as out of scope so far; this spec is that follow-up.

The one technical snag: WebCrypto has X25519 and HKDF but no ChaCha20-Poly1305, which age needs. See "Crypto in the browser".

## Proposal

### 1. Two modes side by side, chosen per check
Both encryption modes stay available (Florent, 2026-11-08). Each check's secrets use one of them:

| Mode | Who encrypts | Regions | Stored in |
|---|---|---|---|
| Server-side (today, the default) | the server: org DEK for shared regions, server-side age sealing for private-only checks | any | `config_private` and/or `config_sealed` (`origin: server`) |
| Browser | dash0 (or any API client), for the agents' public keys | private regions only | `config_sealed` (`origin: browser`) |

- Every org on every tier gets browser encryption. No entitlement, no SaaS gating (Florent, 2026-11-08).
- A check is eligible when every one of its regions is a private region of the org with at least one active agent. dash0 offers browser encryption only then.
- Switching a check from one mode to the other means typing its secrets again. Neither side can convert: the server cannot open a browser blob, and the browser never gets a server-encrypted value back.
- Adding a shared region to a browser-encrypted check makes it ineligible. The form switches it to server-side and asks for the secrets again. The API refuses a region PATCH that would leave a browser blob on a shared region (section 3).
- Org setting `security.browser_sealing.mode` (org-scoped `parameters` row, through `/orgs/:org/settings`, admin only, audit-logged), for orgs that want the full guarantee:
  - `optional` (default): both modes allowed, the choice is per check.
  - `required`: the API refuses plaintext secret fields on checks (`400 SECRETS_MUST_BE_SEALED`), so a check with secrets can only target private regions. Checks without secrets keep any region. The refusal guards API clients: dash0 never sends the plaintext in this mode.
- Switching to `required` does not touch stored data. The settings page lists the checks that do not comply: secrets on a shared or mixed region set, or secrets encrypted by the server (`origin` not `browser`). Each links to its edit page. They keep running until their next secret-bearing write.
- Switching back to `optional` changes nothing stored.

### 2. Recipients endpoint
`GET /api/v1/orgs/:org/sealing-recipients?region=@dc1,@dc2` returns the active agents of those private regions:

```json
{ "data": [ { "agentUid": "…", "agentName": "dc1-a", "region": "@dc1", "recipient": "age1…", "fingerprint": "3f2a…" } ] }
```

- `fingerprint` is `credentials.RecipientFingerprint` (first 8 bytes of SHA-256 of the recipient, hex). It is the X25519 fingerprint, not the Ed25519 one the agent list shows today (`agents/service.go:618-640`). The agents page shows both, labelled.
- The agent prints its recipient fingerprint at startup (and with `SP_AGENT_PRINT_KEYS`) so an operator can compare it with dash0 out of band.
- Any org member who can edit checks can read it. Public keys are not secret.
- A non-private region, or one of another org, is `400 VALIDATION_ERROR`.

### 3. Write API
Check create and PATCH accept a new top-level field next to `config`:

```json
"sealedSecrets": { "keys": ["password"], "fingerprints": ["3f2a…", "9c41…"], "ct": "<base64 age binary>" }
```

The server checks, in this order:
1. Every region of the check is a private region of this org. Else `400 SEALED_SECRETS_REQUIRE_PRIVATE_REGIONS`.
2. `keys` is non-empty, a subset of the checker's `SecretFields()` (`server/internal/crypto/credentials/secret_fields.go:12`), and none of them also appears in `config`.
3. `ct` is valid base64, at most 64 KiB, and an age file whose header (`age.ExtractHeader`, age v1.3.2) holds exactly `len(fingerprints)` X25519 stanzas and nothing else. age hides who a stanza is for, so this is a sanity check, not proof.
4. Sorted `fingerprints` equal the current active recipients of the check's regions (`activeRecipientsForRegions`, `service.go:5914`). Else `409 SEALING_RECIPIENTS_CHANGED`, whose detail carries the current list. dash0 fetches the recipients again, re-seals from the values still in the form and retries once, without bothering the user.

Then it stores `checks.config_sealed = {"v":2,"alg":"age-x25519","fingerprints":[…],"ct":"…","origin":"browser"}`, sets `config_private = null` and `config_private_keys = keys`, and copies the blob to the job rows as today. `origin` is written by the server, never taken from the request. Server-sealed blobs get `"origin":"server"` from now on; a blob without it is a legacy server-sealed one. The agent's `encoding/json` ignores the extra field.

Replacement rules for a browser-sealed check:
- Plaintext secrets in `config` on a browser-encrypted check switch it to server-side (in `optional` mode): the server encrypts them as today and drops the browser blob. They must cover every key of `configPrivateKeys`, for the same reason as below.
- The blob is all-or-nothing. A PATCH without `sealedSecrets` keeps it. A PATCH with `sealedSecrets` replaces it whole and must cover every key of `configPrivateKeys`, unless an explicit `null` in `config` clears that key. Otherwise `400 VALIDATION_ERROR`. The same rule fixes the existing sealed-only PATCH path, which looks like it silently drops secret keys a partial PATCH leaves out (`applyConfigUpdate`, `service.go:5522-5608`, see To verify).
- A PATCH that changes the regions of a check with a browser-sealed blob must bring a new `sealedSecrets` or clear the secrets. Else `409 SEALING_RECIPIENTS_CHANGED`. Today a region-only PATCH does not re-seal (`service.go:2264-2306`).
- Bulk region changes (`bulk_update.go`) skip browser-sealed checks and report them as failed items, with the reason.
- Clone already copies public config only. The clone form asks for the secrets again.

### 4. Crypto in the browser
Produce a real age file, so the agent path does not change. age encrypts the file key per recipient with X25519 + HKDF-SHA256 + ChaCha20-Poly1305 and the payload with ChaCha20-Poly1305 STREAM. WebCrypto covers X25519 (baseline since 2025) and HKDF, not ChaCha20-Poly1305.

Recommended: add `age-encryption` (typage, the TypeScript age implementation by age's author, built on the audited `@noble` libraries, pure JS, no WebAssembly, no `eval`).
- Load it with a dynamic `import()` from the check form only when sealing applies, so other pages do not pay for it.
- The CSP stays as it is: `script-src 'self'`, no `wasm-unsafe-eval` needed.
- Randomness comes from `crypto.getRandomValues`.

Alternative, no new dependency: a WebCrypto-only envelope (X25519 ECDH + HKDF + AES-256-GCM, one wrapped key per recipient) as `alg: "x25519-aesgcm"`, with a new Go opener on the agent. It needs an agent upgrade before any org can use the feature, a hand-written format to review, and two sealing formats to keep forever. See Open questions.

dash0 code: a `web/dash0/src/lib/sealing.ts` with `fetchRecipients(org, regions)` and `sealSecrets(recipients, secrets) -> { keys, fingerprints, ct }`. Fingerprints are computed with `crypto.subtle.digest("SHA-256", …)`, so they match the Go side byte for byte. A Go test and a dash0 unit test share one fixture: the browser output opens with `credentials.UnsealWithIdentity`.

`crypto.subtle` needs a secure context (HTTPS or localhost). A self-hosted dash0 served over plain HTTP on a LAN address cannot seal: the form shows the browser option disabled with the reason, and `required` cannot be selected from such a page.

### 5. dash0
- **Settings, Security tab:** "Check secret encryption": `Server or browser, per check` (optional) or `Browser only` (required), plus the list of checks to fix when `required` is picked (section 1).
- **Check form, any org:**
  - Above the secret inputs, a choice "Encrypted by SolidPing" / "Encrypted in my browser (only your agents can read it)". The browser choice is enabled only when the check is eligible; otherwise a tooltip says why (a shared region selected, no active agent).
  - Editing an existing check shows its current mode. Changing mode opens every secret input empty, to be typed again.
  - Selecting a shared region while the browser mode is on switches back to "Encrypted by SolidPing" and empties the secret inputs, with a notice.
- **Check form, `required` org:** the server-side choice is gone. Once a secret value is entered, shared regions are disabled in the region picker, with a tooltip. Picking a shared region first disables the secret inputs, with the same reason.
- **Browser mode inputs:**
  - A lock badge: "Sealed in your browser for @dc1 (2 agents)". A popover lists agent names and recipient fingerprints.
  - A stored secret shows "Sealed. Only your agents can read it." and a "Replace" button. Replace opens every secret input of the check at once, because the blob is replaced whole. There is never a reveal.
  - With no active agent in the selected private regions, saving is blocked: "Enroll an agent in @dc1 first." The server already refuses this with `ErrNoAgentsToSealTo` (`service.go:181`).
- **Check detail:** a "Browser-sealed" badge next to the credentials summary for browser-mode checks. The existing `needs-reseal-alert.tsx` covers agent changes.
- **Agents page:** the recipient fingerprint, labelled, next to the existing one.
- `data-testid` on every new element (`browser-sealing-mode-select`, `secret-encryption-mode-browser`, `secret-encryption-mode-server`, `sealed-secret-badge`, `sealed-secret-replace-button`, …). Add the sealed secret input state to the design reference page.

### 6. Agent lifecycle
A blob opens only for the agents it was sealed to. The server cannot re-wrap it.

- **New agent joins a region:** existing blobs do not include it. Checks are flagged `needsReseal`, as today for sealed-only checks.
  - Add a claim filter: an org agent is not handed a sealed job whose `fingerprints` lack its own recipient fingerprint. Without it, a third agent in a two-agent region claims jobs it cannot open and the check fails one run in three (see To verify). With it, the old agents keep running the check and nothing goes red.
  - v1: the user re-enters secrets. A "Re-seal" page lists the flagged checks and opens each form in Replace mode.
  - Phase 2, peer re-wrap: an admin clicks "Re-seal for the current agents". The server sends a `rewrap` task to an online agent that is a recipient. That agent opens each blob, seals it to the new recipient list and sends it back. The server only relays ciphertext. The recipient list comes from the server, the same trust the browser already gives it.
- **Agent revoked or deleted:** it can no longer claim, but its key still opens the blobs it was in. Checks are flagged `needsReseal` to drop it. If the host may be compromised, the docs say to rotate the passwords themselves: re-sealing does not un-leak a key.
- **Agent key rotation:** no in-place rotation. Enroll a new agent, re-seal, revoke the old one.
- **Every agent of a region lost:** the secrets are gone. Re-enter them. This is the point of the feature, and the docs say so plainly.
- **Several regions:** one blob for the union of the agents of all the check's private regions, as today.

### 7. Features that read secrets on the server

| Feature | Browser-sealed check |
|---|---|
| Run now, MCP `run_check` | Works. It only marks jobs due and the agent runs them. |
| `/checks/validate`, MCP `validate_check` | Works. Placeholders stand in for sealed values (`service.go:5614-5760`). Secret format errors (a bad PEM) show at the first run. |
| MCP `diagnose_check`, `probe` | Works. Reads the redacted check, or takes ad-hoc input. |
| AI JS generate and repair (`server/internal/aichecks/repair.go:186`) | Runs without secrets, as for sealed-only checks today. The UI says so. |
| Cloud-side SSH tunnel (`server/internal/integrations/sshtunnel/sshtunnel.go:284-306`) | Not possible. A browser-sealed SSH check serves as a tunnel only for checks of the same private regions (the agent-side tunnel frame carries `configSealed`). |
| Export, version snapshots | Unchanged. Secrets are stripped. |
| Import, config-as-code apply | v1: absent secrets are kept, as today. Phase 2: accept `sealedSecrets` in the file. |
| Mixed-region re-seal on enrollment | Does not apply: no `config_private` exists. |
| Notification integrations (`settings_private`) | Out of scope. The server sends the notifications, so it must read those tokens. The docs and the marketing copy say "check credentials", not "all secrets". |

### 8. API clients, CLI, MCP
- Anyone can seal without a browser: fetch the recipients, then `age -r age1… -r age1…` on the JSON of the secret map, base64 it, send `sealedSecrets`. The docs give the recipe.
- Phase 2: `solidping seal --server … --org … --region @dc1 < secrets.json` prints the `sealedSecrets` object (same binary, `credentials.SealForRecipients` already exists).
- MCP `create_check` and `update_check` accept a `sealedSecrets` object as is. Plaintext secrets keep working as today, except in a `required` org, where they are refused, and the message points to dash0 or the CLI. An LLM should not handle the password anyway.

### 9. Migrations
None. The setting is a `parameters` row, `origin` lives in the existing `config_sealed` JSON, `config_private_keys` already exists.

## Threat model
For browser-mode checks only. Server-side mode keeps today's guarantees, no more. A `required` org gets these for every check secret.

Protects against:
- A database dump, backup or replica leak. Already true for server-sealed private-only checks.
- The operator, or anyone with server access, reading request bodies, logs, traces, memory or core dumps: the plaintext never reaches the server. This is what server-side mode cannot give.
- A later compromise of the server: the stored blobs stay closed without an agent key.
- A legal demand on the operator for the customer's check passwords.

Does not protect against:
- A malicious or compromised server serving modified dash0 JavaScript, or swapping recipient keys. It could capture the password as it is typed or seal it to its own key. Mitigations: compare recipient fingerprints with the agent's startup log, and the paranoid path, sealing with the age CLI from a fetched and verified recipient list.
- A compromised browser or browser extension.
- A compromised agent host. It holds the key and runs the check with the plaintext.
- Check results that echo a secret (a target that prints the token in an error message). Unchanged from today.
- Notification integration secrets, which stay server-side.

## Docs
New page `web/docs/docs/features/browser-sealed-secrets.md`, linked from `features/private-locations.md` (its "Region-sealed credentials" part) and `configuration/security.md`:
1. What it does, in three sentences, and the one-line promise.
2. Server-side or browser: a short comparison table (who can read the secret, which regions, what you lose: AI repair with secrets, cloud-side SSH tunnel, recovery when all agents are lost).
3. Requirements: a private location with at least one agent, dash0 over HTTPS, a browser from 2025 or later. Every plan.
4. Choosing the mode in the check form, switching a check between modes (secrets typed again), and the org-wide `required` mode with its list of checks to fix.
5. What changes for a browser-mode check: write-only, replace all secrets together, private regions only.
6. Adding, revoking and losing agents. Re-sealing. Rotate the passwords if an agent host is compromised.
7. Sealing from the API or a script: the recipients endpoint, the age CLI recipe, then `solidping seal` once it exists.
8. What the server still sees: check names, targets, public config, results, notification tokens.
9. Threat model, the short version of the section above.
10. FAQ: "I lost every agent of a region", "Can SolidPing support recover my password?" (no), "Why can't I use a shared region?".

Plus a `CHANGELOG.md` entry per `wiki/conventions/changelog.md`, and an update of `wiki/features/deported-agents.md:611` (browser sealing no longer out of scope).

## Marketing (solidping-website repo, not this one)
- Headline: "Your private agents, your private keys, your private passwords."
- Body: check passwords are encrypted in your browser, for keys that only your own agents hold. SolidPing stores ciphertext it cannot open. Not even we can read them.
- Be exact about scope: check credentials, on private locations. Notification tokens are still used by SolidPing to send alerts. Do not write "zero-knowledge" without that line.
- Say it is an option next to the default server-side encryption, on every plan.
- Place it on the private locations page and the security page.

## Phasing
1. Recipients endpoint, `sealedSecrets` on the write API, `origin`, the per-check mode and the org setting, dash0 sealing, claim filter, docs. Shippable alone.
2. Peer re-wrap, `solidping seal`, `sealedSecrets` in config-as-code.

## Tests
- Go, `credentials`: a blob produced by the dash0 sealing code (fixture committed from a dash0 unit test) opens with `UnsealWithIdentity`. Fingerprints computed in the browser equal `RecipientFingerprint`.
- Go, checks handler, table-driven, SQLite and Postgres:
  - `sealedSecrets` on a private-only check is stored with `origin: browser`, `config_private` null, keys set, and reaches `check_jobs`.
  - Refused: a shared or mixed region set, a key outside `SecretFields()`, a key present in both `config` and `keys`, bad base64, an oversized blob, a stanza count that differs from `fingerprints`.
  - Stale fingerprints give `409 SEALING_RECIPIENTS_CHANGED` with the current list.
  - `optional` org: a server-side check and a browser-mode check coexist; plaintext secrets on a browser-mode check switch it to server-side and clear `config_sealed`; `sealedSecrets` on a server-side private-only check switches it to browser mode.
  - `required` org: a plaintext secret gives `400 SECRETS_MUST_BE_SEALED`; a check without secrets on a shared region still saves.
  - A PATCH that omits a stored secret key is refused; a region-only PATCH of a browser-sealed check is refused.
  - Positive control for each refusal: the same request with the offending part fixed succeeds.
- Go, agentws: the claim filter does not hand a job to an agent missing from its fingerprints, and still hands it to one that is in them.
- Recipients endpoint: active agents only, other orgs' regions refused.
- dash0 Playwright (`web/dash0/e2e/`):
  - Create an HTTP basic-auth check on a private region in browser mode, assert from the network log that the request body holds no plaintext password, reopen the check, see the sealed badge, Replace, save.
  - Same check in server mode still saves as today. Adding a shared region to a browser-mode check switches it to server mode and empties the secret inputs.
  - `required` org: no server-mode choice, shared regions disabled once a secret is typed.

## To verify
- The sealed-only PATCH path: does a PATCH carrying only some secret keys drop the others (`service.go:5522-5608`)? No test covers it. Fix it with the replacement rule above either way.
- Whether a sealed job is ever handed to an agent outside its fingerprints today (`handleClaim`, `server/internal/handlers/agentws/handler.go:1023`, `ClaimJobsForAgent`). The research found no such filter.
- typage: exact package name and version, licence (BSD-3 expected), gzipped size, no `eval` or `new Function` (CSP), whether it calls `crypto.subtle` (secure context) or only `getRandomValues`.
- The agent's envelope decoder tolerates the new `origin` field (no `DisallowUnknownFields`).
- `age.ExtractHeader` in v1.3.2 gives what is needed to count X25519 stanzas, or parse the text header by hand.
- Interplay with the bulk set-regions spec (`2026-11-05-01-bulk-set-check-regions`).
- Every place that writes `config_sealed` (create, PATCH, enroll reseal, import) sets `origin`.

## Open questions
1. **Browser crypto: typage dependency, or a WebCrypto-only format?** Recommended: typage. It keeps the agent untouched and reuses the format already in production. A home-made envelope saves one dependency but costs a second format forever and an agent upgrade before launch.
2. **Default mode in the form for an eligible check (all regions private, agents enrolled)?** Recommended: browser mode pre-selected for new checks. Existing checks keep their mode. Server mode stays one click away.
3. **Keep the org-wide `required` mode?** Recommended: yes. It is a few lines on the server and it is what backs the promise "SolidPing never receives your check passwords" for a whole org.
4. **Checks that do not comply when an org switches to `required`: block the switch, or let them run until edited?** Recommended: let them run, list them on the settings page, refuse their next secret-bearing write. Blocking the switch would force a big cleanup first.
5. **New agent in a region: manual re-entry only in v1, peer re-wrap in phase 2?** Recommended: yes, plus the claim filter in v1 so nothing goes red while checks wait for a re-seal.
6. **Optional shared region key** (one X25519 identity given to every agent of a region, so adding an agent needs no re-seal)? Recommended: not now. It moves key distribution onto the operator and makes revoking one agent mean rotating the region key. Revisit if users complain about re-sealing.
7. **Partial secret replacement: keep one blob per check, or one blob per field?** Recommended: one blob. Most checks have one or two secret fields, and per-field blobs need an agent change.
8. **Notification integration secrets in scope?** Recommended: no. The server must read them to send alerts. Say so in the docs and the marketing copy.
9. **Self-hosted dash0 over plain HTTP (no secure context).** Recommended: browser mode and `required` are disabled from such a page, and the form explains why. Server mode keeps working. No fallback.

## Resolved open questions
- **Server-side and browser encryption both stay available** (Florent, 2026-11-08). The choice is per check. An org can make browser mode mandatory with `security.browser_sealing.mode = required`.
- **No paid-tier gating** (Florent, 2026-11-08). Browser mode is available to every org on every plan, SaaS and self-hosted. No entitlement.
