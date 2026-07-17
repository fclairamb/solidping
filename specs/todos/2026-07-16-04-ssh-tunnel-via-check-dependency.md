---
model: opus
effort: xhigh
---

# Tunnel-capable checks can run through an SSH check's connection (check-dependency SSH tunnels)

## Problem

Customers need to probe services that are only reachable through a bastion host
(private networks, VPC-internal services) without deploying anything new —
they already run SSH. Today SolidPing has no way to route a check's probe
through such a bastion.

This is complementary to the deported-agent spec
(`specs/todos/2026-07-16-02-per-org-deported-agent-websocket.md`): the agent
covers ICMP/UDP and networks with no inbound access at all, while an SSH tunnel
is far lower-friction for the common "there is a bastion" case — nothing to
deploy or enroll. The two can compose later (an agent could itself use a
tunnel), but this spec stands alone.

Current state:

- **There is no dialer seam.** Every checker dials inline with its own
  primitives: `checkhttp` builds a bare `http.Client{}` with no custom
  `Transport` (`server/internal/checkers/checkhttp/checker.go:249`);
  `checktcp` instantiates its own `net.Dialer`
  (`server/internal/checkers/checktcp/checker.go:174`) and — critically —
  resolves DNS locally first (`checker.go:87-89`, `resolver.LookupIPAddr`),
  which can never work for private hostnames.
- **The SSH check config is already a complete tunnel-endpoint definition**:
  `host`, `port`, `timeout`, `expected_fingerprint`, `username`, `password`,
  `private_key` (`server/internal/checkers/checkssh/config.go:14-27`), with
  `password`/`private_key` declared secret
  (`server/internal/checkers/checkssh/secret_fields.go`) and therefore
  encrypted at rest. No new resource type or credential store is needed.
- `golang.org/x/crypto/ssh` is already a dependency (used by `checkssh` and
  `checksftp`); `ssh.Client.DialContext` provides the port-forward primitive.

### Related finding (filed separately, do NOT fix here)

While scoping: the in-process worker never decrypts `config_private` —
`check_jobs` rows carry the split config
(`server/internal/db/postgres/postgres.go:1172-1175`) but nothing in
`server/internal/checkworker/` touches `ConfigPrivate`/`MergeConfig`; the only
decrypt-at-dispatch is on the remote-worker HTTP path
(`server/internal/handlers/workers/service.go:199-228`), which has no
production client. This spec deliberately resolves tunnel credentials through
an execute-time resolver (the k8s/freebox pattern) so it does **not** depend on
that broken merge path. Do not attempt to fix the merge gap in this spec.

## Decisions (settled in design discussion — do not re-litigate)

1. **Model: check-depends-on-check.** A tunnel-capable check carries a
   `tunnelCheckUid` key in its config, referencing an **SSH check** in the same
   org. No new "integration" or standalone tunnel resource: the SSH check is
   the single home for the bastion's credentials, the bastion itself gets
   monitored for free, and the dependency edge is the future hook for
   parent/child alert suppression.
2. **The dependency is config-level, not runtime-level.** Each execution of the
   dependent check dials its own fresh SSH session at execute time; nothing is
   reused from the SSH check's own scheduled runs. Consequently: disabling or
   pausing the SSH check does **not** stop dependents, and the dependent
   check's regions are independent of the SSH check's regions.
3. **Dialer injection uses the `DialContext(ctx, network, addr) (net.Conn,
   error)` shape** (compatible with `golang.org/x/net/proxy.ContextDialer` and
   with what `ssh.Client` exposes) — **not** the concrete `net.Dialer` struct.
4. **Remote-side DNS.** When tunneling, checkers must skip local name
   resolution and pass the raw `host:port` through the dialer — the SSH
   direct-tcpip request carries the hostname and the *bastion* resolves it.
   `checktcp`'s local `LookupIPAddr` is bypassed in tunnel mode.
5. **Capability is declarative metadata, not a hand-maintained list.**
   `CheckTypeMeta` (`server/internal/checkers/checkerdef/types.go:243`) gains a
   `SupportsTunnel bool`; the API serves it so the dashboard gates the selector
   on it. v1 enables **`http` and `tcp` only** — prove the seam on two
   checkers, roll the rest (DB checkers, sftp, …) out in follow-ups.
6. **Tunnel config resolution is server-side at execute time via a
   package-level resolver**, mirroring `checkkubernetes.ClientsetResolverFunc`
   (`server/internal/checkers/checkkubernetes/checker.go:49`, wired at
   `server/internal/app/server.go:310`) and the freebox resolver
   (`server.go:304`). The resolver loads the referenced check row and decrypts
   its `config_private` via `credentials.Service.DecryptForOrg`. When deported
   agents land, private-region jobs will instead need the tunnel config
   embedded (sealed) into the dispatched job — that is an integration point for
   spec 2026-07-16-02, out of scope here, but keep the resolver behind an
   interface so it can be swapped per execution context.
7. **No chaining in v1.** The referenced SSH check must not itself carry a
   `tunnelCheckUid` — rejected at validation time. This kills cycles trivially;
   multi-hop bastions are a possible follow-up.
8. **Host-key verification is required for tunnel use.** An SSH check may be
   used as a tunnel only if its `expected_fingerprint` is set; the tunnel dial
   uses strict host-key checking against it. Never
   `ssh.InsecureIgnoreHostKey()` on the tunnel path. (A plain SSH *check*
   without a fingerprint keeps working as today — the requirement applies only
   when it is referenced as a tunnel.)
9. **Latency semantics.** The check's measured `duration` starts **after** the
   tunnel is established; tunnel setup time is recorded as a separate metric
   (`tunnel_setup_ms`) in the result `metrics` JSONB. Otherwise every tunneled
   check's latency graph is dominated by SSH handshakes.
10. **Failure classification.** A tunnel failure (resolve, dial, auth,
    host-key mismatch, forward rejected) produces a **distinct error output**
    (`"tunnel failed: …"`, plus a machine-readable marker in the result
    output) vs a failure of the target behind the tunnel. This is the
    groundwork for later dependency-aware suppression.
11. **Field name is tunnel-specific** (`tunnelCheckUid`), not a generic
    `dependsOn` — the semantic is "dial through this". A generic dependency
    concept can arrive later and subsume the edge.

## Proposal

### 1. `checkerdef`: dialer type + context plumbing + capability meta

- `ContextDialer` interface: `DialContext(ctx context.Context, network, addr
  string) (net.Conn, error)`.
- Context helpers `WithTunnelDialer(ctx, ContextDialer)` /
  `TunnelDialerFrom(ctx) ContextDialer` (nil when absent). Checkers *consume*
  the dialer from context; they never resolve tunnels themselves.
- `CheckTypeMeta.SupportsTunnel bool` (`checkerdef/types.go:243`), set to true
  for `http` and `tcp` in their meta registrations; exposed through the
  check-types listing the dashboard already consumes
  (`ListCheckTypeMetas`, `types.go:309`).
- A well-known config key constant for `tunnelCheckUid`, following the
  `timeout` precedent (`checkTimeoutConfigKey`,
  `server/internal/checkworker/worker.go:913`): the worker reads it generically
  from the raw config map; checkers' `FromMap` tolerate/ignore it. No
  per-checker config-struct field.

### 2. Tunnel resolver package

New package (suggested: `server/internal/integrations/sshtunnel`):

- `ResolverFunc(ctx, orgUID, tunnelCheckUID) (checkerdef.ContextDialer,
  io.Closer, error)` as a package-level var (the k8s/freebox pattern), wired
  once in `server.go` next to `server.go:304-310` with `dbService` + the
  credentials service.
- Resolution: load the check by uid, require same org + type `ssh` + not
  deleted + `expected_fingerprint` set + no `tunnelCheckUid` of its own;
  decrypt `config_private` (`DecryptForOrg` + `credentials.MergeConfig`); build
  `ssh.ClientConfig` (auth from `private_key` or `password`,
  `ssh.FixedHostKey`-style verification against `expected_fingerprint` — reuse
  however `checkssh` verifies fingerprints today so the formats match); dial
  with the SSH check's timeout; return a dialer wrapping
  `ssh.Client.DialContext` plus a closer for the client.
- Every failure returns a typed error so the worker can classify it as a
  tunnel failure (decision 10).

### 3. Worker injection (`server/internal/checkworker/worker.go`)

In `executeJob`, after config parsing (~`worker.go:729`): if the raw config map
carries `tunnelCheckUid`, resolve via the package-level resolver, `defer
close`, wrap `ctx` with `WithTunnelDialer`, and record tunnel setup duration
into the result metrics as `tunnel_setup_ms`. Resolution or dial failure →
save an error result with the distinct tunnel-failure output; the target probe
never runs. Passive check types are exempt (they make no outbound requests).

### 4. Checker changes (v1: `checkhttp`, `checktcp`)

- `checkhttp` (`checker.go:249`): when `TunnelDialerFrom(ctx)` is non-nil,
  build the client with `&http.Transport{DialContext: dialer.DialContext}`.
  TLS keeps working unchanged — `http.Transport` performs its own TLS handshake
  over the tunneled conn, with `ServerName` from the URL host. Redirect policy
  and everything else unchanged.
- `checktcp` (`checker.go:87-89`, `:174`): when the tunnel dialer is present,
  **skip the local `LookupIPAddr`** (decision 4) and call
  `dialer.DialContext(ctx, "tcp", host:port)` directly; without it, behavior is
  byte-for-byte today's.

### 5. Validation (`server/internal/handlers/checks/service.go`)

On both create (before `applyEncryption`, ~`service.go:998`) and PATCH (on the
`applyConfigPatch` output, ~`service.go:1296-1305` — note `UpdateCheck` never
calls `checker.Validate`, so service-level validation is the only gate on the
PATCH path): if the effective config carries `tunnelCheckUid`, require:

- the check's own type has `SupportsTunnel` in its meta,
- the referenced check exists in the same org, is type `ssh`, is not deleted,
- it has `expected_fingerprint` set,
- it does not itself carry `tunnelCheckUid` (no chaining).

Violations → `VALIDATION_ERROR` (400) with a message naming the failed rule.

**Delete guard**: deleting an SSH check referenced as a tunnel by other checks
→ `409 CONFLICT` listing the dependent check slugs. Implement the dependent
lookup with a JSONB query on PG (`config->>'tunnelCheckUid'`) and
`json_extract` on SQLite; both paths tested.

### 6. Dashboard (`web/dash0` — start from the design reference per CLAUDE.md)

- **Check form**: for check types whose meta has `supportsTunnel`, an optional
  "Run through SSH tunnel" `<Select>` listing the org's SSH checks —
  follow the freebox connection-selector pattern
  (`web/dash0/src/components/checks/form/types/infra.tsx`, `FreeboxLineFields`)
  including the empty-state link ("No SSH checks yet — create one"). Selecting
  writes `tunnelCheckUid` into the config; "None" clears it. Disable (with
  hint) SSH checks lacking `expected_fingerprint`.
- **Check detail**: a tunneled check shows a badge/link to its SSH check; the
  SSH check's detail page lists "used as tunnel by N checks" (this also
  explains the delete 409 to users).
- Mobile-usable, existing primitives only; if a needed primitive is missing,
  add it to the design reference page as part of the change.

### 7. Docs

A `web/docs` feature page: what tunneling does, which check types support it,
the fingerprint requirement, latency semantics (`tunnel_setup_ms`), and the
failure-classification behavior. Update the check-types page's http/tcp rows.

## Testing

- **Unit — resolver**: missing check, wrong org, wrong type, deleted, no
  fingerprint, chained tunnel → typed errors; dialer built from `private_key`
  and from `password`; fingerprint mismatch rejected.
- **Unit — checkers**: an in-process test SSH server (built on
  `golang.org/x/crypto/ssh` server support, handling `direct-tcpip` channels)
  forwarding to a local `httptest`/TCP listener: `checkhttp` and `checktcp`
  succeed through the tunnel against a hostname only the "bastion" resolves
  (assert no local resolution happened); without a dialer in context, behavior
  unchanged (regression).
- **Integration (`handlers/checks`, PG + SQLite)**: create ssh check + http
  check with `tunnelCheckUid`; all validation rejections above; delete guard
  409 with dependent slugs; PATCH of an unrelated field preserves
  `tunnelCheckUid`; worker path — tunnel dial failure produces the distinct
  tunnel-failure result and `tunnel_setup_ms` appears in metrics on success.
- **E2E (Playwright, `web/dash0/e2e/`)**: selector visible only on
  tunnel-capable types; create a tunneled check end-to-end; SSH-check detail
  shows the dependents list.
- Full `make test`, `make test-dash`, `make lint`, `make fmt`. Re-run failing
  PG tests with `-p 1` before treating a testcontainer flake as a regression.

## Follow-ups (not in scope)

- Roll `SupportsTunnel` out to the remaining TCP-dialing checkers (DB checkers,
  `checksftp`, mail checkers, …) once the seam is proven.
- Pooled/persistent `ssh.Client` per (tunnel, worker) with health-checked reuse
  — v1 dials per execution by design.
- Dependency-aware alert suppression ("bastion down → suppress dependents"),
  building on the failure classification and the `tunnelCheckUid` edge.
- Deported-agent integration: embedding (sealed) tunnel config into dispatched
  jobs for private regions (spec 2026-07-16-02 phase 2 interaction).
- Multi-hop tunnels (chained bastions).

## Implementation Plan

Derived from the Proposal (§1–7) and the 11 Decisions; the numbering below maps
each step to the Proposal section it implements.

### Step 1 — `checkerdef`: dialer seam + capability meta (Proposal §1, D3/D5)

- New `checkerdef/tunnel.go`:
  - `ContextDialer` interface — `DialContext(ctx, network, addr string) (net.Conn, error)`
    (D3: the func shape, never `*net.Dialer`).
  - `WithTunnelDialer(ctx, ContextDialer)` / `TunnelDialerFrom(ctx) ContextDialer`
    (nil when absent). Checkers only *consume*; they never resolve.
  - `TunnelCheckUIDConfigKey = "tunnelCheckUid"` well-known key + a
    `TunnelCheckUIDFrom(configMap)` reader, mirroring `checkTimeoutConfigKey`.
    No per-checker config-struct field; `FromMap` ignores it.
  - `OutputKeyTunnelFailed` marker + `TunnelFailureOutput` helper (D10).
- `CheckTypeMeta.SupportsTunnel bool` (`json:"supportsTunnel"`), true for `http`
  and `tcp` only (D5). Exposed via `checktypes.CheckTypeResponse.supportsTunnel`.

### Step 2 — `internal/integrations/sshtunnel` resolver (Proposal §2, D6/D7/D8)

- `Resolver func(ctx, orgUID, tunnelCheckUID) (checkerdef.ContextDialer, io.Closer, error)`,
  package-level `ResolverFunc` var + `WithResolver(ctx, …)` override (the
  k8s/freebox pattern, kept behind an interface so a future agent execution
  context can swap it — D6).
- `NewResolver(loader CheckLoader, creds Decryptor)`: narrow interfaces (not
  `db.Service`) so unit tests need no database. Wired in `app/server.go` next to
  the freebox/k8s resolvers with `dbService` + the credentials service.
- Resolution rules → typed sentinel errors: not found / wrong org (same lookup),
  wrong type, deleted, missing `expected_fingerprint` (D8), own `tunnelCheckUid`
  (no chaining, D7).
- Decrypt `config_private` via `DecryptForOrg` + `credentials.MergeConfig`;
  plaintext-fallback path honored (secrets stay on the public map).
- `ssh.ClientConfig`: `private_key` or `password` auth, strict
  `SHA256:<base64>` fingerprint callback reusing checkssh's format, never
  `InsecureIgnoreHostKey`. Dial honors the SSH check's timeout and the ctx.
- Dialer wraps `ssh.Client.DialContext`; closer closes the client.

### Step 3 — Worker injection (Proposal §3, D2/D9/D10)

`checkworker/worker.go` `executeJob`, after config parse / `execCtx` creation:
if the raw job config carries `tunnelCheckUid` → resolve (fresh session per
execution, D2), `defer closer.Close()`, `execCtx = WithTunnelDialer(...)`,
measure setup and record `tunnel_setup_ms` into the result metrics (D9 — the
checker measures its own `Duration`, so setup is excluded by construction).
Resolve/dial failure → distinct tunnel-failure error result
(`"tunnel failed: …"` + `tunnel_failed: true` marker); the probe never runs.
Passive types return before this point, so they are exempt.

### Step 4 — Checkers (Proposal §4, D4)

- `checkhttp`: when `TunnelDialerFrom(ctx) != nil`, set
  `client.Transport = &http.Transport{DialContext: dialer.DialContext}`. TLS,
  redirects, everything else unchanged.
- `checktcp`: when the dialer is present, **skip** `LookupIPAddr` and dial the
  raw `host:port` through it (D4 — the bastion resolves the name). Without a
  dialer the path is byte-for-byte today's.

### Step 5 — Validation + delete guard (Proposal §5)

- `validateTunnelConfig(ctx, orgUID, checkType, effective)` in
  `handlers/checks/service.go`, called on **both** paths: `CreateCheck` (after
  `normalizeCheckConfig`, before `applyEncryption`) and `applyConfigUpdate`
  (on the merged+normalized config — the only gate on PATCH). Rules: own type
  `SupportsTunnel`; referenced check exists in-org, type `ssh`, not deleted, has
  `expected_fingerprint`, is not itself tunneled. Violations → `*ConfigError` →
  400 `VALIDATION_ERROR` naming the failed rule.
- Delete guard: new `db.Service.ListChecksByTunnelCheckUID(ctx, orgUID, uid)` —
  `config->>'tunnelCheckUid'` on PG, `json_extract(config, '$.tunnelCheckUid')`
  on SQLite. `DeleteCheck` on an `ssh` check with dependents → `ErrTunnelInUse`
  → 409 `CONFLICT` listing the dependent slugs.

### Step 6 — Dashboard (Proposal §6)

- `CheckTypeInfo.supportsTunnel` in `api/hooks.ts`.
- `check-form.tsx`: a shared (protocol-agnostic) tunnel selector layered like
  the `timeout` key — visible only when the active type's meta says
  `supportsTunnel`. Lists the org's SSH checks (freebox selector pattern,
  incl. the empty-state link); "None" clears; SSH checks with no
  `expected_fingerprint` are disabled with a hint.
- Check detail: tunneled check → badge/link to its SSH check; SSH check detail →
  "used as tunnel by N checks" list (explains the 409).
- Start from the design reference; existing primitives only; mobile-usable.

### Step 7 — Docs (Proposal §7)

`web/docs` feature page (what it does, supported types, fingerprint
requirement, `tunnel_setup_ms` latency semantics, failure classification) +
the check-types page's http/tcp rows.

### Step 8 — Tests (Testing section)

- Unit — resolver: every rejection rule → typed error; dialer from `private_key`
  and from `password`; fingerprint mismatch rejected.
- Unit — checkers: in-process `x/crypto/ssh` server handling `direct-tcpip`,
  forwarding to a local `httptest`/TCP listener; `checkhttp` + `checktcp` succeed
  through the tunnel against a hostname only the "bastion" resolves, asserting
  **no local resolution** happened; no-dialer regression path unchanged.
- Integration (`handlers/checks`, PG + SQLite): create ssh + tunneled http;
  every validation rejection; delete-guard 409 with slugs; PATCH of an unrelated
  field preserves `tunnelCheckUid`.
- Worker: tunnel dial failure → distinct result; `tunnel_setup_ms` in metrics on
  success.
- E2E (dash0): selector visibility per type, create a tunneled check, dependents
  list on the SSH check.
