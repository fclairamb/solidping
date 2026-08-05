---
model: opus
effort: high
---

# The deported agent logs its own private keys at INFO on every fresh enrollment

## Problem

`persistIdentity` in
[`server/internal/agentmode/agentmode.go:172-175`](../../server/internal/agentmode/agentmode.go)
unconditionally logs the agent's full identity JSON, base64-encoded, at INFO:

```
msg="To run this agent from environment only (e.g. a Kubernetes secret), set SP_AGENT_KEYS to the value below" SP_AGENT_KEYS="<base64>"
```

That payload decodes to a JSON object containing `ed25519PrivateKey` and
`x25519Identity` (an `AGE-SECRET-KEY-1…` value) alongside `agentUid` and
`region`. The package doc comment (`agentmode.go:9-11`) states this is
deliberate — "It ALWAYS logs the base64 of the same JSON" — so this is a design
choice, not an oversight, which is why it must be fixed in code *and* in every
doc that tells operators to harvest it from the logs.

Why this is a real vulnerability, not a nit:

- **It contradicts the stated security property.**
  [`wiki/features/deported-agents.md:56-58`](../../wiki/features/deported-agents.md)
  claims "The private keys never leave the agent. The server stores public keys
  only." On any platform that aggregates stdout — fly, Kubernetes, Docker,
  systemd-journald — the keys leave the agent the moment it enrolls, and are
  forwarded to whatever log drain is configured.
- **The x25519 identity is the credential-decryption key.** Check credentials
  (`config_private`) are re-sealed per-agent at claim time with
  `filippo.io/age` to exactly this recipient. Leaking the identity leaks the
  ability to decrypt every credential ever sealed to that agent.
- **The ed25519 private key is the agent's whole authentication.** There is no
  bearer token; reconnects are authenticated purely by an Ed25519 signature
  over `method|path|timestamp|nonce`. Whoever has that key *is* the agent.
- **It fires on every fresh enrollment.** On fly that is every `fly deploy`,
  because machines and volumes are recreated.

Observed live on **2026-08-04** on fly app `solidping-agent-nrt` (region
`jp-1`): the keys are in fly's log stream now.

The docs propagate the anti-pattern — they instruct operators to copy the value
out of the logs:

- [`web/docs/docs/features/private-locations.md:63`](../../web/docs/docs/features/private-locations.md)
  — "**and** logs the same JSON base64-encoded so you can run env-only instead"
- `web/docs/docs/features/private-locations.md:82-83` — "copy the
  `SP_AGENT_KEYS` value from the agent's logs into a Secret"
- [`web/dash0/src/routes/orgs/$org/organization.private-locations.register.tsx:654`](../../web/dash0/src/routes/orgs/$org/organization.private-locations.register.tsx)
  — "copy the `SP_AGENT_KEYS` value the agent logs on first start into a Secret"

(`web/docs/build/` is generated output — do not hand-edit it.)

## Proposal

### 1. Never log key material by default

Rewrite `persistIdentity` so the default path logs only **where** the identity
lives, never the identity itself:

- Keep writing the JSON to `cfg.Agent.KeysFile` (fallback `./agent-keys.json`),
  mode `0600`, and keep the existing `"Agent identity persisted", "path", path`
  INFO line — that one is fine.
- **Delete** the unconditional `"SP_AGENT_KEYS", encoded` INFO line.
- When the file could not be written anywhere, keep a WARN, but reword it so it
  points at the opt-in below instead of implying the value was printed.

### 2. Explicit opt-in escape hatch for env-only bootstrap

The log line exists for a genuine need: env-only deployments (k8s Secret, fly
app-wide secret) need the base64 once, and on a container with no writable
volume there may be no file to read. Preserve that, but make it a deliberate,
loud, one-time operator action rather than the default:

- New env var **`SP_AGENT_PRINT_KEYS=true`** (wire it in
  `server/internal/config/config.go` next to the hand-rolled `SP_AGENT_KEYS` /
  `SP_AGENT_KEYS_FILE` reader — koanf's underscore→dot collapsing means it
  needs the same manual treatment — and register it in
  `server/internal/config/envvars.go`).
- Only when it is `true` does the agent emit the base64. Emit it to **stdout
  directly, not through slog**, wrapped in an unmistakable banner
  (`!!! PRIVATE KEY MATERIAL — this will be captured by your log aggregator !!!`),
  so a structured-log drain does not silently index it as a normal field and so
  the operator cannot mistake it for routine output. Always accompany it with a
  WARN through the logger saying that key material was printed because
  `SP_AGENT_PRINT_KEYS` is set — that WARN carries no key material.

### 3. Document the safe bootstrap path

The primary documented way to get `SP_AGENT_KEYS` becomes: read the file the
agent already wrote.

```bash
# docker
docker exec <container> base64 -w0 /data/agent-keys.json
# fly
fly ssh console -a solidping-agent-nrt -C "base64 -w0 /data/agent-keys.json"
# kubernetes (first run with a temporary PVC)
kubectl exec deploy/solidping-agent -- base64 -w0 /data/agent-keys.json
```

Update, consistently:

- `wiki/features/deported-agents.md` — the enrollment steps and the env-var
  table (add `SP_AGENT_PRINT_KEYS`); keep the "private keys never leave the
  agent" claim and make it *true* by noting the single opt-in exception.
- `web/docs/docs/features/private-locations.md` — step 3 wording, the env-var
  table, and the Kubernetes section's "copy from the logs" instruction.
- `deploy/fly/README.md` — the env-var table (line ~118) and the verification
  section (line ~181); document the `fly ssh console` extraction, and note that
  because fly secrets are app-wide, per-machine `SP_AGENT_KEYS` is not usable
  for multi-machine agent apps (already noted in
  `specs/done/2026/07/2026-07-27-01-fly-io-system-agents.md:45`).
- `web/dash0/src/routes/orgs/$org/organization.private-locations.register.tsx:654`
  — replace "copy the value the agent logs" with the read-the-file recipe. Per
  the repo's frontend rules, check the design reference before touching this
  page even for a copy change.
- The `agentmode` package doc comment (`agentmode.go:6-11`), which currently
  documents the ALWAYS-log behaviour as intended.

### 4. Audit for other key-material logging

Sweep `server/internal/agentmode/`, `server/internal/agents/`,
`server/internal/checkworker/backend/`, `server/internal/crypto/`, and the
enrollment-token mint path for any log/print of a private key, an
`AGE-SECRET-KEY-1…` value, an `spe_` enrollment token, or a decrypted
credential at any level (not just INFO — a DEBUG leak is still a leak on a
platform running at debug). Record the findings in the spec's follow-up notes
even if the sweep comes back clean, so the negative result is documented.

### 5. Regression test

Add `server/internal/agentmode/agentmode_test.go` with a test that installs a
`slog` handler over a `bytes.Buffer` **and** captures stdout, runs
`persistIdentity` with a known generated identity, and asserts that the
captured output contains **none** of:

- the raw base64 of the identity JSON,
- the `ed25519PrivateKey` value,
- the `x25519Identity` value / the literal `AGE-SECRET-KEY-1` prefix.

Give it a **positive control**: the same test with `SP_AGENT_PRINT_KEYS=true`
must find the base64 on stdout. Without that control, the negative assertion
would pass trivially if the capture plumbing were broken. Cover both the
file-writable and file-unwritable branches — the unwritable branch is the one
that historically justified the log line and is the likeliest place for a leak
to survive. Follow the repo's test conventions (`testify/require`,
`t.Parallel()`).

### 6. Rotate the already-exposed fly agent

`solidping-agent-nrt` (region `jp-1`) has had its identity published to fly's
log stream, so the fix is not complete until that identity is dead. After the
code fix ships:

1. Revoke the agent row from the dashboard (Private locations → agents →
   revoke) so the old Ed25519 public key can no longer authenticate.
2. Destroy the machine **and its volume** (the volume is what carries
   `/data/agent-keys.json`) so it cannot re-enroll with the leaked keypair.
3. Mint a fresh enrollment token and redeploy; confirm the new agent enrolls
   and that the logs contain the path line and **no** base64.
4. Any check whose credentials were sealed to the old x25519 identity should be
   treated as exposed — re-seal happens automatically on the next claim by the
   new agent, but the underlying **credentials themselves should be rotated by
   their owner**, since anyone holding the leaked identity could have decrypted
   them. Call this out explicitly rather than assuming re-sealing is sufficient.

This is an operational step, not a code change — the implementer should perform
steps 1-3 if they have fly access, and otherwise leave a clearly-flagged
checklist for the operator.

## Open questions

- Should `SP_AGENT_PRINT_KEYS` also be honoured on *every* start (not just
  first enrollment), so an operator can recover the value from an
  already-running agent without shell access? Leaning yes — it is opt-in and
  the alternative is re-enrolling.
- Is a CHANGELOG entry under a `Security` heading warranted for the release
  that carries this? Leaning yes, with no exploit detail beyond "the agent no
  longer logs its private keys; rotate any agent enrolled before this version".

## Implementation Plan

1. **Config** — add `PrintKeys bool` (`koanf:"print_keys"`) to `AgentConfig`, read
   `SP_AGENT_PRINT_KEYS` in `applyAgentEnv` (bool parse), register it in
   `envvars.go`.
2. **agentmode** — rewrite `persistIdentity` to take an `io.Writer` for key
   output (wired to `os.Stdout` by `Run`, a buffer in tests): keep the file
   write + "Agent identity persisted" INFO, delete the unconditional
   `SP_AGENT_KEYS` INFO line, reword the unwritable WARN to point at
   `SP_AGENT_PRINT_KEYS`. Emit the base64 only when `cfg.Agent.PrintKeys`, via
   `fmt.Fprintf` to the writer inside a `!!! PRIVATE KEY MATERIAL … !!!` banner,
   always accompanied by a key-free WARN. Honour the flag on *every* start
   (Open question 1, "leaning yes"): `Run` prints for an already-persisted
   identity too, and enrollment prints via the persist callback (no double
   print — the two paths are mutually exclusive). Rewrite the package doc.
3. **Tests** — new `server/internal/agentmode/agentmode_test.go`: negative tests
   (writable + unwritable branch) asserting neither slog output nor the key
   writer contains the base64 / ed25519 private key / x25519 identity /
   `AGE-SECRET-KEY-1`; positive control with `PrintKeys=true`; a sequential test
   that redirects the real `os.Stdout` around `Run`-level plumbing to catch
   stray `fmt.Print`; plus a config test for `SP_AGENT_PRINT_KEYS`.
4. **Audit sweep** (Proposal §4) — grep `agentmode/`, `agents/`,
   `checkworker/backend/`, `crypto/`, and the enrollment-token mint path for
   logging of private keys, `AGE-SECRET-KEY-1`, `spe_` tokens or decrypted
   credentials at any level; record findings (including a clean result) in this
   spec's follow-up notes.
5. **Docs** — `wiki/features/deported-agents.md`,
   `web/docs/docs/features/private-locations.md`, `deploy/fly/README.md`, and
   the dash0 register page copy: replace "copy it from the logs" with the
   read-the-file recipe, add `SP_AGENT_PRINT_KEYS` to the env-var tables, note
   the app-wide-secret caveat on fly. `web/docs/build/` is generated — untouched.
6. **Operational** (Proposal §6) — leave a flagged rotation checklist for
   `solidping-agent-nrt` in this spec and in `deploy/fly/README.md`; no fly CLI
   actions are performed here.
7. **QA** — `make build-backend lint-back test`; dash0 gate for the copy change.

## Follow-up notes (implementation)

### Audit sweep (Proposal §4) — result: CLEAN, one finding fixed

Swept for any log/print of a private key, an `AGE-SECRET-KEY-1…` value, an
`spe_` enrollment token, or a decrypted credential, at **any** level:

| Area | Result |
|---|---|
| `server/internal/agentmode/` | **The one real leak** — `persistIdentity`'s unconditional INFO line. Fixed. Remaining log lines carry only the path, the agent UID and the region. |
| `server/internal/agents/` (`crypto.go`, `protocol.go`, `nonce.go`) | Clean — no logger at all. Enrollment tokens are minted and returned, never logged; only the SHA-256 is persisted (`db/models/agent.go` comments this explicitly). Errors wrap causes, never key bytes. |
| `server/internal/checkworker/backend/` | Clean — `direct.go:140` and `ws.go:258` log decrypt/unseal *failures* with check/job/org UIDs and the error only; the comment at `direct.go:136` already states the never-log-a-config-value contract. `ws.go:708` logs `agent_uid` + region. |
| `server/internal/crypto/credentials/` | Clean — the package imports no logger and prints nothing. |
| Enrollment-token mint path (`handlers/agents/`, `handlers/agentws/`, `app/systemagents.go`) | Clean — `systemagents.go:104` explicitly notes it never logs the token; malformed-entry errors log the region only. Auth failures return static messages, never the presented token. |

Repo-wide negative control: `grep -rn "PrivateKey\|X25519Identity\|AGE-SECRET"`
intersected with any logging/printing call returns **zero** non-test hits.

### CHANGELOG (Open question 2)

`CHANGELOG.md` is generated end-to-end by release-please (`release-please-config.json`,
`release-type: simple`) — there is no `Unreleased` section to hand-edit, and a manual
entry would be overwritten by the next release PR. The fix therefore ships as a
`fix(agent):` commit so it lands under **Bug Fixes** in the release notes; the release
PR's changelog polish step should phrase it as: *the agent no longer logs its private
keys; rotate any agent enrolled before this version*. No `Security` heading is
configured in `changelog-sections` — adding one would be a separate config change.

### Operator checklist — rotate the exposed fly agent (Proposal §6) — NOT DONE

**No fly CLI operation was performed by this implementation.** `solidping-agent-nrt`
(region `jp-1`) published its identity to fly's log stream on 2026-08-04 and is still
exposed. The full checklist now also lives in `deploy/fly/README.md` ("Rotating an
exposed agent identity"):

1. Revoke the agent (dashboard → Private locations → agents → revoke).
2. Destroy the machine **and its volume** — the volume carries `/data/agent-keys.json`.
3. Mint a fresh enrollment token, redeploy, confirm the new agent enrolls and that its
   logs show the path line and no base64.
4. **Rotate the underlying check credentials** sealed to the old x25519 identity.
   Automatic re-sealing to the new agent protects only future traffic; anyone holding
   the leaked identity could already have decrypted the existing secrets.
