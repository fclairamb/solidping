---
sidebar_position: 12
title: Browser-Sealed Secrets
---

# Browser-sealed check secrets

Your private agents, your private keys, your private passwords.

A check that runs only on your [private locations](./private-locations.md) can have its secrets encrypted in your browser, for keys that only your agents hold. SolidPing receives and stores ciphertext it cannot open. Not even we can read those passwords.

This covers **check credentials on private locations**. Notification tokens (Slack, webhooks, email) are still read by SolidPing, because it is the one sending the alerts.

## Server-side or browser

Both modes are available on every plan, SaaS and self-hosted. You choose per check.

| | Encrypted by SolidPing | Encrypted in my browser |
|---|---|---|
| Who can read the secret | SolidPing (in memory, while it runs your check or seals it for your agents) | Only your agents |
| Regions | Any | Private locations only |
| AI repair with secrets | Yes | Runs without the secrets |
| Cloud-side SSH tunnel | Yes | Only for checks of the same private locations |
| Recovery when every agent is lost | Not needed | None: type the secrets again |

## Requirements

- A private location with at least one enrolled agent, or a shared region key.
- dash0 over HTTPS (or `localhost`). Over plain HTTP the browser option is disabled and explains why.
- A browser from 2025 or later.

## Choosing the mode

In the check form, the **Authentication & secrets** section shows *Encrypted by SolidPing* and *Encrypted in my browser*. The browser choice is pre-selected for a new check whose regions are all private. It is disabled, with the reason, as soon as a shared region is selected: the form switches to *Encrypted by SolidPing* and tells you so.

Switching an existing check from one mode to the other means typing its secrets again. Neither side can convert: the server cannot open a browser blob, and the browser never gets a server-encrypted value back.

### Requiring browser encryption

**Organization → Settings → Check secret encryption**: *Server or browser, per check* (default) or *Browser only*. In *Browser only*, the API refuses plaintext secret fields (`400 SECRETS_MUST_BE_SEALED`), so a check with secrets can only target private locations. Checks without secrets keep any region.

Switching to *Browser only* does not touch stored checks. The settings card lists the ones to fix (secrets on a shared region, or encrypted by SolidPing), with a link to each. They keep running until their next secret-bearing save.

## What changes for a browser-sealed check

- Secrets are write-only, as before. There is never a reveal.
- The secrets are write-only. The form keeps them closed until you click **Replace**, which opens every secret input at once.
- The secrets are replaced together: a save with new secrets must include all of them (or explicitly clear the ones you drop).
- Private locations only. Changing the regions of a browser-sealed check requires typing the secrets again (`409 SEALING_RECIPIENTS_CHANGED`). Bulk region changes skip these checks and report them.
- The check page shows a **Browser-sealed** badge.

## Key per agent or shared region key

Each private location seals either:

- **Key per agent** (default): to every active agent's own key. A new agent cannot open the existing blobs. It is not handed those jobs (the other agents keep running them) and the checks are flagged *needs re-seal* until you save their secrets again. **Private locations > Re-seal** lists those checks, each opening its form ready to type the secrets again.
- **Shared region key**: to one region key that every agent of the location holds. Scale out, replace a host or restart from a fresh volume without re-sealing anything.

To use a shared key: **Organization → Private locations → Use a shared region key**. The browser generates the key, shows the secret `AGE-SECRET-KEY-1…` once (copy or download it, and keep a copy in your password manager) and sends only the public `age1…` half to SolidPing. Give the secret to every agent of the location:

```bash
SP_AGENT_REGION_KEYS=AGE-SECRET-KEY-1...      # comma-separated
SP_AGENT_REGION_KEYS_FILE=/run/secrets/region-keys   # one key per line, # comments
```

The agent logs the fingerprint of each key it loads and advertises it to the server (never the key). The location page warns when no active agent holds the current key.

**Offline key:** to keep the region key away from the browser, generate it on a machine you trust and paste only its public key with *Paste a public key*:

```bash
solidping agent region-key generate > region-keys.txt
# public key: age1…
# fingerprint: 3f2a…
# AGE-SECRET-KEY-1…  (give this line to the agents)
```

The output is a valid `SP_AGENT_REGION_KEYS_FILE`.

**Rotation:** *Rotate the region key*, add the new key to every agent next to the old one, click *Re-seal for the current agents* (or save the location's checks again), then remove the old key from the agents. The page counts the checks still sealed to an older key.

**Region keys generated in the dashboard on v0.47.0 must be rotated** (advisory GHSA-c9qq-4m99-c7f6). On v0.47.0 the region key dialog was recorded by PostHog session replay, so on SolidPing SaaS, and on any instance with `SP_POSTHOG_PROJECT_API_KEY` set, the `AGE-SECRET-KEY-1…` secret may sit in the PostHog project. Generate a new key, give it to the agents, re-seal (*Re-seal for the current agents*), then remove the old key from the agents. Keys pasted with *Paste a public key* never reached the browser and are not affected. The private locations page shows a notice on every shared key created before the fix until you dismiss it. Since the fix, the dashboard keeps every secret it shows (region keys, tokens, signing secrets, recovery codes) out of session replay and autocapture, and pauses the recording while the region key dialog is open.

## Re-sealing on an agent

When an agent joins a location, or the region key changes, the checks sealed before are flagged. *Re-seal for the current agents* (on the location page, the Re-seal page, and the warning of a flagged check) fixes them without typing the secrets again:

1. The server picks an online agent that holds a key the secrets were sealed to (its own key, or a region key it loaded).
2. **Possession challenge.** The server seals a random value to that one key, and to nothing else, and hands it to the agent with the secrets. The agent opens it and sends it back with the new ciphertext. The server keeps only the value's hash. A public key is no proof: an agent that registered a copy of another agent's key, or advertises a region key it does not hold, cannot open the challenge, and its result is refused (`invalid_result`). The check keeps its old secrets.
3. The agent opens the secrets, checks they only carry secret fields of the check type (and the [binding](#binding-to-the-target) of this check), seals them for the current agents (or the current region key), and returns only the new ciphertext.
4. The server checks it like a sealed write (one key slot per recipient, the current fingerprints, the size limit).
5. **Second agent.** Before storing it, the server sends the new ciphertext to another online agent that holds a current key, with its own possession challenge. That agent opens it, checks it only carries secret fields of the check type, and, when it can open the old secrets too, that they did not change. On its answer the server stores the new ciphertext. The check keeps its *Encrypted in the browser* status. If the second agent cannot open it, or refuses it, nothing is stored and the reason is shown.

With no second agent online, the new ciphertext is stored, but the check stays flagged with the reason *Not confirmed by a second agent*. Re-seal again once two agents are online, or type the secrets again. A single-agent location therefore always ends there: that is the price of not taking one agent's word for it.

The server relays ciphertext only; the recipient list comes from the server, the same trust the browser already gives it. A check stays flagged, with the reason shown, when no online agent can open it, when no agent finishes within 5 minutes, or when the returned ciphertext is refused. Then type its secrets again.

When a region key is rotated or removed, the server keeps its public key (never the identity), so the agents that still hold the old key can be challenged on it. A key rotated before version 0.48 is not on record: its checks show "the agents that hold these secrets cannot prove it", and you type their secrets again.

An agent cannot enroll with an X25519 key that is already another live agent's key, or a region key of the organization: the enrollment is refused, and the token stays valid for a retry with a new identity.

**Turning it off.** The agent seals to the recipient list the server sends. A compromised server could add its own key to that list and read the secrets on the next re-seal. To rule that out, set `SP_AGENT_REWRAP_ENABLED=false` on every agent of the location: the agent then never re-seals, and only the agent's operator can change that, not the server. Flagged checks show "the agents that hold these secrets do not run the re-seal", and you type their secrets again. Each re-seal an agent does is logged on the agent (`Sealed secrets re-wrapped`) with the fingerprints it sealed to, so an unknown key stands out.

API: `POST /api/v1/orgs/:org/sealing/rewrap` with `{"region":"@dc1"}` or `{"checkUids":["…"]}` (every flagged check without a body), then `GET /api/v1/orgs/:org/sealing/rewrap?check=<uid>,<uid>` until each status is `done` or `failed`. Admins only. Agents need version 0.48 or later (an older agent cannot answer the possession challenge: its checks show "do not run the re-seal").

## Adding, revoking and losing agents

- A revoked agent's key still opens the blobs it was in. Save the secrets again to drop it.
- In a shared location, a revoked agent's copy of the region key opens every blob of the location: rotate the region key.
- If an agent host may be compromised, rotate the passwords themselves. Re-sealing does not un-leak a key.
- If every agent of a location is lost (and, with a shared key, every copy of it), the secrets are gone. Type them again.

## Sealing from the API or a script

With the `solidping` binary and a Personal Access Token:

```bash
echo '{"basicAuth":"alice:hunter2"}' | SP_TOKEN=… \
  solidping seal --server https://solidping.example.com --org acme --region @dc1 \
    --type http --config-json '{"url":"https://db.acme.com/health"}'
# {"keys":["basicAuth"],"fingerprints":["3f2a…"],"ct":"<base64 age file>","bound":true}
```

`--type` and `--config-json` (the check's public config, or `@file.json`) are required: the secrets are bound to that target (see [Binding to the target](#binding-to-the-target)).

Or with the `age` CLI:

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  "https://solidping.example.com/api/v1/orgs/acme/sealing-recipients?region=@dc1"
# {"data":[{"kind":"agent","region":"@dc1","recipient":"age1…","fingerprint":"3f2a…"}],"browserSealingMode":"optional"}

echo -n '{"basicAuth":"alice:hunter2"}' | age -r age1… | base64 -w0
```

Then send it next to `config` on the check create or PATCH:

```json
"sealedSecrets": { "keys": ["basicAuth"], "fingerprints": ["3f2a…"], "ct": "<base64 age file>", "bound": true }
```

With the `age` CLI, add the `_binding` to the plaintext yourself (below) and send `"bound": true`. A blob without one is accepted for now and listed on the Re-seal page.

`fingerprints` must be the current recipients, else `409 SEALING_RECIPIENTS_CHANGED` with the current list. A config-as-code document (`/checks/import`, `/checks/apply`, `sp checks apply`) takes the same `sealedSecrets` object on each check; a stale one is an item error with code `SEALING_RECIPIENTS_CHANGED`, in the dry run too. Exports never contain secrets. The MCP tools `create_check` and `update_check` accept the same object. Compare the fingerprints with the ones your agents log at startup (`Agent sealing key`).

## Binding to the target

The sealed plaintext carries a reserved `_binding` next to the secrets: the check type and a SHA-256 digest of the check's *target fields*, the config keys that decide where the secrets go. Each check type lists them as `targetFields` in `GET /api/v1/orgs/:org/check-types`: `url` for HTTP, host, port and database for the database checks, host and port for TCP, SMTP, SSH and FTP, the script for JS checks, and the tunnel (`tunnelCheckUid`) for every type.

```json
{"basicAuth": "alice:hunter2", "_binding": {"type": "http", "digest": "<hex sha256>"}}
```

The agent recomputes the digest from the config of each job. When it differs, or the type does, it does not run the check and reports *sealed secrets were sealed for another target: type them again*. A blob may also only carry secret fields of its type, so it can never replace the URL or any other public field.

So whoever can write the stored config (a compromised server, someone with write access to the database) cannot point a sealed check at a host they control, or copy its blob onto another check: the binding sits inside the ciphertext, which nobody but the agents can open or change. This holds with `SP_AGENT_REWRAP_ENABLED=false` too, and a re-seal on an agent copies the binding as it is: it never rebinds to a target the server sends.

On your side:

- Changing a target field (the URL, host, port, database or tunnel) of a sealed check needs the secrets typed again. The check form opens the secret inputs on its own; the API answers `409 SEALED_TARGET_CHANGED` to a PATCH without new `sealedSecrets`, and a config-as-code document gets an item error with that code, in the dry run too. Other edits (timeout, assertions, name, period) keep the blob.
- Checks sealed before the binding existed (v0.47.0) still run, with a warning in the agent log, and the Re-seal page lists them as *Seal again to bind to the target*. Type their secrets again to bind them. A later release will refuse unbound blobs.

The digest is computed over a canonical JSON of the target fields that carry a value, keys sorted, numbers and booleans written as strings, `url` with its scheme and host lowercased and the default port made explicit, `host` lowercased. The dashboard and the Go code share a test vector (`web/dash0/src/lib/__fixtures__/target-binding.json`).

The binding does not cover a target the attacker already controls (it binds a name, not who answers it), DNS of the target host, or the TLS settings of the check.

## What the server still sees

Check names, targets, public config, results, and notification tokens. The server can still edit the targets, but the agents refuse sealed secrets once a target changes (see [Binding to the target](#binding-to-the-target)).

## Threat model

Protects against a database or backup leak, the operator reading request bodies, logs or memory (the plaintext never reaches the server), a later compromise of the server, and a legal demand on the operator for your check passwords.

It also protects against a server that points a sealed check at another host, or copies its secrets onto another check: the secrets are bound to their target (see [Binding to the target](#binding-to-the-target)).

Does not protect against a server that serves modified dashboard JavaScript or swaps recipient keys (compare fingerprints, or seal with the `age` CLI), a compromised browser or extension, a compromised agent host, a region key copied somewhere unsafe, a target that echoes a secret in its error message, or notification secrets.

## FAQ

**I lost every agent of a location.** Enroll new agents and type the secrets again.

**I lost the region key.** Generate a new one, give it to the agents, and type the secrets again.

**Can SolidPing support recover my password?** No.

**Why can't I use a shared region?** A shared region runs on SolidPing's workers, which would have to read the secret.
