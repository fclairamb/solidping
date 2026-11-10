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
- The secrets are replaced together: a save with new secrets must include all of them (or explicitly clear the ones you drop).
- Private locations only. Changing the regions of a browser-sealed check requires typing the secrets again (`409 SEALING_RECIPIENTS_CHANGED`). Bulk region changes skip these checks and report them.
- The check page shows a **Browser-sealed** badge.

## Key per agent or shared region key

Each private location seals either:

- **Key per agent** (default): to every active agent's own key. A new agent cannot open the existing blobs. It is not handed those jobs (the other agents keep running them) and the checks are flagged *needs re-seal* until you save their secrets again.
- **Shared region key**: to one region key that every agent of the location holds. Scale out, replace a host or restart from a fresh volume without re-sealing anything.

To use a shared key: **Organization → Private locations → Use a shared region key**. The browser generates the key, shows the secret `AGE-SECRET-KEY-1…` once (copy or download it, and keep a copy in your password manager) and sends only the public `age1…` half to SolidPing. Give the secret to every agent of the location:

```bash
SP_AGENT_REGION_KEYS=AGE-SECRET-KEY-1...      # comma-separated
SP_AGENT_REGION_KEYS_FILE=/run/secrets/region-keys   # one key per line, # comments
```

The agent logs the fingerprint of each key it loads and advertises it to the server (never the key). The location page warns when no active agent holds the current key.

**Rotation:** *Rotate the region key*, add the new key to every agent next to the old one, save the location's checks again, then remove the old key from the agents. The page counts the checks still sealed to an older key.

## Adding, revoking and losing agents

- A revoked agent's key still opens the blobs it was in. Save the secrets again to drop it.
- In a shared location, a revoked agent's copy of the region key opens every blob of the location: rotate the region key.
- If an agent host may be compromised, rotate the passwords themselves. Re-sealing does not un-leak a key.
- If every agent of a location is lost (and, with a shared key, every copy of it), the secrets are gone. Type them again.

## Sealing from the API or a script

```bash
curl -s -H "Authorization: Bearer $TOKEN" \
  "https://solidping.example.com/api/v1/orgs/acme/sealing-recipients?region=@dc1"
# {"data":[{"kind":"agent","region":"@dc1","recipient":"age1…","fingerprint":"3f2a…"}],"browserSealingMode":"optional"}

echo -n '{"basicAuth":"alice:hunter2"}' | age -r age1… | base64 -w0
```

Then send it next to `config` on the check create or PATCH:

```json
"sealedSecrets": { "keys": ["basicAuth"], "fingerprints": ["3f2a…"], "ct": "<base64 age file>" }
```

`fingerprints` must be the current recipients, else `409 SEALING_RECIPIENTS_CHANGED` with the current list. The MCP tools `create_check` and `update_check` accept the same object. Compare the fingerprints with the ones your agents log at startup (`Agent sealing key`).

## What the server still sees

Check names, targets, public config, results, and notification tokens.

## Threat model

Protects against a database or backup leak, the operator reading request bodies, logs or memory (the plaintext never reaches the server), a later compromise of the server, and a legal demand on the operator for your check passwords.

Does not protect against a server that serves modified dashboard JavaScript or swaps recipient keys (compare fingerprints, or seal with the `age` CLI), a compromised browser or extension, a compromised agent host, a region key copied somewhere unsafe, a target that echoes a secret in its error message, or notification secrets.

## FAQ

**I lost every agent of a location.** Enroll new agents and type the secrets again.

**I lost the region key.** Generate a new one, give it to the agents, and type the secrets again.

**Can SolidPing support recover my password?** No.

**Why can't I use a shared region?** A shared region runs on SolidPing's workers, which would have to read the secret.
