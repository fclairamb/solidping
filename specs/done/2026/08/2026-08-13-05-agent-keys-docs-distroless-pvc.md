---
model: sonnet
effort: medium
---

# Agent identity docs tell operators to run `base64` inside a distroless image, which cannot work

## Problem

[`web/docs/docs/features/private-locations.md:79-97`](web/docs/docs/features/private-locations.md)
("Getting the `SP_AGENT_KEYS` value") and its internal twin
[`wiki/features/deported-agents.md:72-93`](wiki/features/deported-agents.md)
("Bootstrapping `SP_AGENT_KEYS`") both instruct operators to extract the agent's
persisted identity by running a command *inside* the agent container:

```bash
docker exec <container> base64 -w0 /data/agent-keys.json
fly ssh console -a <app> -C "base64 -w0 /data/agent-keys.json"
kubectl exec deploy/solidping-agent -- base64 -w0 /data/agent-keys.json
```

None of that works with the shipped image. The runtime stage of the Dockerfile is
[`gcr.io/distroless/base-debian13:nonroot`](Dockerfile:136) — no shell, no
coreutils (so no `base64`), no `tar`. Consequences:

- `kubectl exec … base64 …` → `exec: "base64": executable file not found in $PATH`.
- `kubectl cp` → also fails: it streams through `tar` **inside the container**,
  which distroless does not ship.
- `fly ssh console` → needs `hallpass` in the image; not present.

The doc is therefore actively misleading on the one step an operator hits first,
and the surviving workaround it offers is the bad one:
`SP_AGENT_PRINT_KEYS=true` writes the agent's **private key material** — the
Ed25519 identity key and the X25519 key that decrypts every credential sealed to
the location — to stdout, i.e. straight into the cluster log pipeline. The docs
already say that output makes the agent compromised if a log drain retained it,
yet the exec routes being broken makes it the de-facto only Kubernetes path.

The genuinely correct pattern is the opposite one: **never extract the keys**.
Persist `/data` on a volume, let the agent generate its identity in-pod on first
start, and leave `SP_AGENT_KEYS` unset. This has been validated in production —
see `~/code/acme/exp-devops/solidping/agent-deployment-s3ns-prod.yaml`
(the `@acme/s3ns-prod` private location), which hit exactly this wall and moved
to a PVC. That manifest also surfaces a detail the docs never mention: distroless
`:nonroot` runs as **uid 65532**, so the pod needs `securityContext.fsGroup:
65532` or the agent cannot write `agent-keys.json` (mode `0600`) onto the mounted
volume at all.

## Proposal

Rewrite the identity/bootstrap material in the two source docs. (`web/docs/build/`
and `server/internal/app/docsres/` are generated output — do not hand-edit.)

### 1. `web/docs/docs/features/private-locations.md`

**a. Replace the "Getting the `SP_AGENT_KEYS` value" section.** Drop all three
exec one-liners. Lead with the recommendation — *the identity is generated in the
pod/container and should stay there* — and state plainly that the image is
distroless (no shell, no `base64`, no `tar`), so `kubectl exec` / `kubectl cp` /
`fly ssh console` cannot be used to read `/data/agent-keys.json`.

Keep an extraction path only where one actually works, and verify each before
documenting it:

- **Docker**: `docker cp <container>:/data/agent-keys.json -` is daemon-side and
  does not need `tar` in the container; with a named volume, a helper container
  (`docker run --rm -v agent-data:/data alpine base64 -w0 /data/agent-keys.json`)
  also works. Confirm both against the real image before publishing.
- **Kubernetes**: no in-cluster extraction path exists — say so, and point at the
  PVC section instead.
- **`SP_AGENT_PRINT_KEYS=true`**: keep it, but demote it to a clearly-labelled
  last resort, retaining the existing warning (unset + restart afterwards; treat
  the agent as compromised and re-enroll if a log drain retained the banner).

**b. Rework "Kubernetes (env-only keys)" into two documented options**, PVC first:

- **Recommended — PVC-backed identity.** A full, copy-pasteable manifest modelled
  on `agent-deployment-s3ns-prod.yaml`, carrying its load-bearing details:
  - a `PersistentVolumeClaim` (`ReadWriteOnce`, ~1Gi) mounted at `/data`;
  - `securityContext: { runAsNonRoot: true, fsGroup: 65532 }` on the pod, with a
    comment explaining that 65532 is the distroless `nonroot` uid and that
    without the `fsGroup` the `0600` keys file cannot be written;
  - `strategy: { type: Recreate }` — RWO volume, never two pods on it at once;
  - `SP_AGENT_ENROLLMENT_TOKEN` from a Secret with `optional: true`, so the
    Secret can be deleted after the one-shot enrollment is consumed and the pod
    still schedules;
  - `SP_AGENT_KEYS` deliberately **unset** (it takes precedence over the file —
    see [`config.go:183`](server/internal/config/config.go:183));
  - a note that losing the PVC loses the identity: the agent must then be revoked
    server-side and re-enrolled with a fresh token;
  - the bootstrap sequence: create the enrollment Secret → apply → watch for
    `Agent identity persisted` in the logs and the agent going active in the
    dashboard → delete the enrollment Secret.
- **Alternative — `SP_AGENT_KEYS` + Secret**, for environments with no volume
  available. Keep the existing manifest, but be honest about how the value is
  obtained: extract it from a Docker/local first run, or accept the
  `SP_AGENT_PRINT_KEYS` exposure. Note the HA angle stays valid here — several
  agents in one location, one token each.

**c.** Keep the existing HA paragraph, and reconcile it with the `Recreate` /
RWO constraint: HA means *several agents each with their own identity* (own PVC
or own Secret), never two replicas sharing one keys volume.

### 2. `wiki/features/deported-agents.md`

Apply the same correction to "Bootstrapping `SP_AGENT_KEYS`" (lines ~72-93):
remove the three broken commands, state the distroless constraint and why
`kubectl cp` fails too, and point at the PVC pattern as the default. This file is
the internal engineering note, so it should additionally record *why* — the
distroless runtime stage is a deliberate choice, and the security consequence is
that `SP_AGENT_PRINT_KEYS` must not become the standard Kubernetes bootstrap.

### 3. Consistency pass

- Sweep the rest of both docs (and any other `.md` outside `web/docs/build/` and
  `server/internal/app/docsres/`) for surviving `exec … base64` / `kubectl cp`
  advice against the agent image.
- Check the `SP_AGENT_KEYS_FILE` / `SP_AGENT_KEYS` / `SP_AGENT_PRINT_KEYS` rows
  in the env-var table still read correctly next to the new guidance — in
  particular that `SP_AGENT_KEYS` **wins over** the file, which is exactly why
  the PVC manifest must leave it unset.
- Docs build must pass (`web/docs` Docusaurus build); no broken relative links.

### Out of scope

Code changes. This is a documentation fix only — no new agent flag, no
key-export subcommand. (A `solidping agent export-keys`-style command would be
the real ergonomic fix for the env-only path and is worth its own spec, but it is
not this one.)
