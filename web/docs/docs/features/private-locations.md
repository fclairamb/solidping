---
sidebar_position: 11
title: Private Locations & Agents
---

# Private locations & deported agents

Monitor infrastructure that is **not reachable from SolidPing's cloud workers** —
private networks, on-prem datacenters, VPC-internal services — by running an
**org-scoped deported agent** inside your own network.

Key properties:

- **Outbound-only WebSocket** — the agent dials out to the SolidPing server;
  no inbound ports, no VPN, firewall-friendly.
- **One-shot enrollment** — the agent enrolls with a single-use `spe_` token,
  generates its own keypairs locally, and sends only the public halves to the
  server. The server never stores a usable agent credential.
- **Checks-only by construction** — the agent's protocol is exactly
  *claim jobs / submit results*. It cannot read configuration, list checks, or
  touch any other org data, and the server enforces this — it does not trust
  the agent.
- **Region-sealed credentials** — secrets for checks that target *only* your
  private location are encrypted (with [age](https://age-encryption.org)
  X25519) to your agents' public keys **at write time**. The server cannot
  decrypt them afterwards.

## 1. Create a private location

In the dashboard: **Organization → Private locations → Add location**. A
location is an org-private region with a slug like `dc1`; its region identity
is `@<slug>` (e.g. `@dc1`) — org-relative, because the organization is already
implied by the URL and by every row that stores it, so **renaming your
organization never breaks a private location**. Cloud workers can structurally
never match an `@…` region — private locations are served exclusively by your
agents.

> The older fully-qualified spelling `@<org>/<slug>` is still accepted on input
> (for your organization's current or previous slug) and is normalized away;
> existing installs are rewritten automatically on upgrade.

## 2. Mint an enrollment token

On the location's row, mint an **enrollment token** (`spe_…`). It is shown
**exactly once** (the server stores only its hash), enrolls **exactly one
agent**, and expires after 24 hours by default.

## 3. Run the agent

The agent is the standard SolidPing container in agent mode — no separate
binary, no database:

```bash
docker run -d --name solidping-agent \
  -v agent-data:/data \
  -e SP_NODE_ROLE=agent \
  -e SP_AGENT_SERVER_URL=https://solidping.example.com \
  -e SP_AGENT_ENROLLMENT_TOKEN=spe_… \
  ghcr.io/fclairamb/solidping:latest
```

On first start the agent:

1. generates an **Ed25519 identity keypair** (used to sign every reconnect)
   and an **X25519 encryption keypair** (credentials are sealed to it),
2. connects with the enrollment token and enrolls (the token is consumed
   atomically — it can never enroll a second agent),
3. persists its identity to `/data/agent-keys.json` (`SP_AGENT_KEYS_FILE`,
   mode `0600`).

The agent logs the *path* it wrote, never the keys themselves: that file holds
the private key material that authenticates the agent and decrypts every
credential sealed to it, and container stdout ends up in your log aggregator.

Every later connection authenticates with an Ed25519 signature over
`method|path|timestamp|nonce` (±5 minutes of clock skew, replay-protected) —
there is no bearer token to steal, and the database only ever holds public keys.

### Getting the `SP_AGENT_KEYS` value

To run an agent from the environment alone, read the file the agent already
wrote:

```bash
# docker
docker exec <container> base64 -w0 /data/agent-keys.json
# fly
fly ssh console -a <app> -C "base64 -w0 /data/agent-keys.json"
# kubernetes (first run with a temporary volume)
kubectl exec deploy/solidping-agent -- base64 -w0 /data/agent-keys.json
```

If the agent has no readable file at all (no writable volume, no shell), start
it once with `SP_AGENT_PRINT_KEYS=true`. It then prints the base64 to stdout
inside a `!!! PRIVATE KEY MATERIAL !!!` banner. Copy it into your secret store,
then unset the variable and restart — and treat the agent as compromised (revoke
and re-enroll it) if that output was retained by a log drain.

### Environment variables

| Variable | Default | Purpose |
|---|---|---|
| `SP_NODE_ROLE` | — | Set to `agent` to enable agent mode |
| `SP_AGENT_SERVER_URL` | — | The SolidPing server base URL (required) |
| `SP_AGENT_ENROLLMENT_TOKEN` | — | One-shot `spe_` token (first run only) |
| `SP_AGENT_KEYS_FILE` | `/data/agent-keys.json` | Where the identity JSON is persisted |
| `SP_AGENT_KEYS` | — | Base64 identity JSON for env-only deployments (wins over the file) |
| `SP_AGENT_NAME` | hostname | Display name shown in the dashboard |
| `SP_AGENT_PRINT_KEYS` | `false` | Prints the agent's **private key material** to stdout — opt-in bootstrap only (honoured on every start); unset it again afterwards |

### Kubernetes (env-only keys)

After the first enrollment, read the `SP_AGENT_KEYS` value out of the agent's
keys file (`kubectl exec deploy/solidping-agent -- base64 -w0
/data/agent-keys.json`) and store it in a Secret — the pod then needs no
persistent volume:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: solidping-agent
spec:
  replicas: 1
  selector:
    matchLabels: { app: solidping-agent }
  template:
    metadata:
      labels: { app: solidping-agent }
    spec:
      containers:
        - name: agent
          image: ghcr.io/fclairamb/solidping:latest
          env:
            - name: SP_NODE_ROLE
              value: agent
            - name: SP_AGENT_SERVER_URL
              value: https://solidping.example.com
            - name: SP_AGENT_KEYS
              valueFrom:
                secretKeyRef: { name: solidping-agent-keys, key: keys }
```

For **high availability**, enroll several agents into the same location (one
token each). They share the work through the same lease mechanism cloud
workers use — if one agent dies, its leases expire and a sibling picks the
checks up.

## 4. Target the location from a check

The check form's region picker lists your private locations alongside the
cloud regions (badged **Private**). A check may target:

- **only private location(s)** — its secret fields (passwords, tokens) are
  stored *sealed-only*: encrypted to the location's active agents. The server
  cannot decrypt them after the write.
- **a mix of private and cloud regions** — secrets are dual-stored: the
  standard server-side envelope for cloud dispatch *plus* the sealed blob for
  your agents.

:::caution Enroll the agent first
Saving credentials on a check that targets **only** private locations requires
at least one active agent there — the write is rejected with a validation error
otherwise. There is no fallback: storing the secrets server-side "for now"
would break the sealed-only guarantee, and the check could not run anyway
without a blob its agents can open. Enroll the agent, then save the check.

(A **mixed** private+cloud check has no such restriction: the cloud side legitimately
needs the server-side envelope, so it saves normally and is flagged
*needs re-seal* until an agent exists.)
:::

### Updating a sealed check's configuration

Sealed-only credentials are invisible to the server, which shapes how `PATCH`
behaves:

- **Secrets absent from the request** — the existing sealed blob is kept
  **exactly as-is**. The server cannot decrypt it, so it cannot merge a partial
  change into it; leaving it untouched is the only safe option. Non-secret
  fields (URL, headers, period…) update normally.
- **Secrets present in the request** — the whole blob is replaced by a fresh
  one sealed to the location's currently-active agents.

So there is no way to change *one* secret field of a sealed-only check while
leaving the others alone: re-send every secret the check needs, or none of
them. (This is a property of the encryption, not a limitation of the API — the
same is true of any zero-knowledge store.)

### Re-sealing

The sealed blob names the exact agents it was encrypted to, so agent
membership changes matter:

- **A new agent joins the location** — mixed-mode checks are re-sealed
  automatically. Sealed-only checks can't be (the server can't read them):
  they are flagged **needs re-seal** — as `needsReseal: true` on the check's
  API detail response and as a warning banner on the check's page in the
  dashboard — and the new agent reports a clear job error (*"credentials not
  sealed for this agent — re-save the check's credentials"*). Re-saving the
  check's credentials fixes it.
- **An agent is revoked** — it loses access immediately (its live connection
  is closed and reconnects get 403), and mixed-mode checks are re-sealed
  without it. **Honest caveat:** a revoked agent already saw the credentials
  that were sealed to it while it was active — treat them as exposed and
  rotate them.

## Security model

| Property | Mechanism |
|---|---|
| Agent can't read org data | WS protocol is claim/result only; enforced server-side |
| Agent can't claim foreign work | Claims are hard-scoped to the agent's org **and** exact region |
| No stealable agent credential | Ed25519 signature auth; DB stores public keys only |
| Enrollment can't be replayed | Single-use token, atomic consume, hash-only at rest |
| Server can't read sealed-only secrets | age X25519 multi-recipient encryption to agent keys |
| Runaway agent can't flood | Per-org check-rate entitlement enforced at dispatch |

### Known limitations

Stated plainly, because a security feature's caveats matter more than its
marketing:

- **Reconnect replay protection is per-instance.** Each agent reconnect is
  signed over `method|path|timestamp|nonce`, and the server remembers recent
  nonces to reject replays. That memory is **local to the server instance that
  handled the connection**. If you run SolidPing as multiple replicas behind a
  load balancer, an attacker who captures a signed handshake could replay it
  against a *different* replica within the ±5-minute skew window and open one
  connection as that agent. The remaining guards still hold — the connection is
  still scoped to that agent's org and region, still can only claim/submit
  checks, and cannot decrypt any credential without the agent's X25519 private
  key, which never leaves the agent. Single-instance deployments are unaffected.
  A shared (e.g. Redis/Postgres-backed) nonce store would close the multi-replica
  gap; until then, prefer terminating agent connections on a single replica if
  this is in your threat model.
- **`jobs-available` hints are per-instance too.** An agent connected to replica
  A is only nudged by check-creation events observed on replica A. This is a
  latency optimization, not a correctness mechanism: the agent's regular claim
  poll picks the work up regardless.
- **Revocation is not retroactive.** See the rotation caveat above — revoking an
  agent stops it from receiving *new* work and *future* credentials, but
  anything it already decrypted is already known to it.
