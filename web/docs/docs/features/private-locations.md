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

The identity is generated **in the container on first start** and is meant to
stay there — prefer the [PVC-backed Kubernetes option](#kubernetes) below over
extracting it into an environment variable at all.

The agent image's runtime stage is distroless
(`gcr.io/distroless/base-debian13:nonroot`): no shell, no `base64`, no `tar`.
That rules out the commands that look like the obvious first move:

- `kubectl exec … base64 …` fails: `exec: "base64": executable file not found
  in $PATH`.
- `kubectl cp` fails too — it streams the copy through `tar` running **inside
  the container**, and distroless doesn't ship that either.
- `fly ssh console` needs `hallpass` baked into the image; it isn't there.

**Kubernetes has no in-cluster extraction path.** Use the
[PVC-backed identity](#kubernetes) option instead: the agent keeps its own
identity on a volume and you never touch the keys file at all.

**Docker** is the one place extraction still works, because `docker cp` reads
the container's filesystem from the daemon side — it doesn't execute anything
inside the container, so the missing shell/`base64`/`tar` don't matter:

```bash
# straight from the daemon — no shell or tar needed inside the container
docker cp <container>:/data/agent-keys.json - | tar -xO | base64 -w0

# or, against a named volume with no running container at all:
docker run --rm -v agent-data:/data alpine base64 -w0 /data/agent-keys.json
```

**Last resort — `SP_AGENT_PRINT_KEYS=true`.** If neither of the above is an
option, start the agent once with `SP_AGENT_PRINT_KEYS=true`. It prints the
base64 identity to stdout inside a `!!! PRIVATE KEY MATERIAL !!!` banner —
i.e. into whatever aggregates your container logs. Copy it into your secret
store, then unset the variable and restart — and treat the agent as
compromised (revoke and re-enroll it) if that output was retained by a log
drain.

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

### Kubernetes

Two supported patterns, in order of preference.

#### Recommended — PVC-backed identity

Give the agent its own volume and let it generate and keep its identity
in-pod, the same way the Docker example above does. Nothing to extract,
nothing to put in a Secret except the one-shot enrollment token.

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: solidping-agent-keys
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: solidping-agent
spec:
  replicas: 1
  # The keys PVC is ReadWriteOnce: never run two pods against it at once.
  strategy:
    type: Recreate
  selector:
    matchLabels: { app: solidping-agent }
  template:
    metadata:
      labels: { app: solidping-agent }
    spec:
      securityContext:
        runAsNonRoot: true
        # distroless :nonroot runs as uid 65532. Without this fsGroup the
        # mounted volume stays owned by root and the agent can't write the
        # 0600 agent-keys.json file onto it at all.
        fsGroup: 65532
      containers:
        - name: agent
          image: ghcr.io/fclairamb/solidping:latest
          env:
            - name: SP_NODE_ROLE
              value: agent
            - name: SP_AGENT_SERVER_URL
              value: https://solidping.example.com
            # First start only — ignored once /data/agent-keys.json exists.
            - name: SP_AGENT_ENROLLMENT_TOKEN
              valueFrom:
                secretKeyRef:
                  name: solidping-agent-enrollment
                  key: token
                  optional: true
            # SP_AGENT_KEYS is deliberately unset here: it takes precedence
            # over the keys file, so setting it would defeat the PVC.
          volumeMounts:
            - name: keys
              mountPath: /data
      volumes:
        - name: keys
          persistentVolumeClaim:
            claimName: solidping-agent-keys
```

Bootstrap sequence:

1. Create the one-shot enrollment Secret out of band:
   ```bash
   kubectl create secret generic solidping-agent-enrollment \
     --from-literal=token=spe_…
   ```
2. `kubectl apply -f` the manifest above.
3. Watch the logs until `Agent identity persisted` appears and the agent
   shows up **active** in the dashboard — that means the token was consumed
   and the identity is written to the PVC.
4. Delete the enrollment Secret. `optional: true` on the env var keeps the
   pod schedulable without it:
   ```bash
   kubectl delete secret solidping-agent-enrollment
   ```

**Losing the PVC loses the identity.** There is no way to recover it — revoke
the agent server-side and re-enroll it with a fresh token.

#### Alternative — `SP_AGENT_KEYS` from a Secret

For environments with no volume available, store the identity directly in a
Secret instead — the pod then needs no persistent volume:

```yaml
            - name: SP_AGENT_KEYS
              valueFrom:
                secretKeyRef: { name: solidping-agent-keys, key: keys }
```

You still need to get the base64 value in the first place, and Kubernetes
gives you no way to do that in-cluster (see above) — extract it from a
Docker/local first run (the `docker cp` recipe above) or accept the
`SP_AGENT_PRINT_KEYS` exposure, then store the result in the Secret.

#### High availability

Enroll several agents into the same location — each with its **own**
identity (own PVC or own Secret) and one enrollment token each. They share
the work through the same lease mechanism cloud workers use — if one agent
dies, its leases expire and a sibling picks the checks up. Never point two
pod replicas at the same PVC or the same keys Secret; that's exactly what
`strategy: { type: Recreate }` on the PVC option guards against.

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
