---
model: opus
effort: high
---

# `${param:}` has no API to create the parameter, `import` sends references literally, and a resolved body is stored in the public config

## Problem

The config-as-code story for secrets is documented in
[`wiki/features/config-as-code.md`](wiki/features/config-as-code.md) §"Secret
references": write `${env:NAME}` or `${param:KEY}` in any config string value,
"resolved server-side at apply time … into the existing encrypted
`config_private` envelope. The committed file stays secret-free."

Trying to use it for a real check — an HTTP `POST` to a Keycloak token endpoint
whose form body must carry a password (exp-devops `sso-keycloak-login`,
2026-09-11) — hit four gaps in a row:

1. **There is no way to create a `param`.** `resolveParamRef`
   ([`apply.go:323`](server/internal/handlers/checks/apply.go:323)) reads
   org-scoped parameters first, then system-wide. The DB layer has
   `GetOrgParameter`/`SetOrgParameter`, and the credentials package uses them
   internally for the DEK store
   ([`credentials/param_store.go:49`](server/internal/crypto/credentials/param_store.go:49)).
   But the only HTTP routes are `/api/v1/system/parameters` — super-admin only
   ([`server.go:1541`](server/internal/app/server.go:1541)). An org admin on
   SaaS cannot create the parameter the wiki tells them to reference.
   `${env:}` is the fallback, and it means an env var on the API pod — a
   deploy per secret, per org, which is not a SaaS story.

2. **`import` does not resolve references.** Only `ApplyChecks` calls
   `resolveSecretRefs` ([`apply.go:393`](server/internal/handlers/checks/apply.go:393));
   `ImportChecks` ([`service.go:3507`](server/internal/handlers/checks/service.go:3507))
   never does. The two endpoints take the same document, so a file that
   `apply` handles correctly is stored *literally* by `import` — the probe then
   sends the string `${env:SP_SSO_AUTHTEST_PASSWORD}` to the target. The
   existing external tooling (exp-devops `solidping_config.py`) uses `/import`
   and had to grow a guard that refuses documents containing references.

3. **A resolved value lands in the public config.** `resolveSecretRefs` does
   `cfg[key] = resolved` ([`apply.go:250`](server/internal/handlers/checks/apply.go:250))
   on the raw config; `applyEncryption` later splits out `SecretFields()` only.
   `body` is not a secret field, so a password resolved into a form body is
   stored in the public `config` column, returned by `GET /checks/:uid` and by
   `/checks/export` in plaintext. The wiki sentence quoted above is not true for
   any key outside `SecretFields()` — and the whole point of a reference in a
   `body` is that `body` is not one.

4. **Export re-emits the value, not the reference.** Even if (3) were fixed,
   `export → edit → apply` cannot round-trip a referenced value: the exporter
   has no memory that a key was reference-derived, so the next export either
   leaks it or (once redacted) drops it, and the next apply wipes it.

Together: the documented mechanism is unusable by an org admin, silently wrong
through one of the two endpoints, and a plaintext leak through the other.

## Proposal

### 1. Org parameters API

- `GET /api/v1/orgs/:org/parameters`, `GET|PUT|DELETE /api/v1/orgs/:org/parameters/:key`
  — org admin. `PUT` body `{ "value": <string>, "secret": <bool> }` mirroring
  [`SetParameterRequest`](server/internal/handlers/system/service.go:98).
  A `secret: true` parameter is returned as `{ "key", "secret": true,
  "updatedAt" }` with no value; the list endpoint never returns secret values.
- Keys: `^[a-z][a-z0-9_.-]{0,63}$`. Reserve a `sp.` prefix for the internal
  DEK-store keys the credentials package writes, and refuse it over the API so
  an org cannot clobber its own encryption material.
- Dashboard: **Organization → Parameters** page (list, add, rotate, delete),
  built from the design reference. Secret values are write-only in the UI too.
- `sp params list|set|delete` in the CLI.

### 2. Reference resolution belongs to the document pipeline, not to `apply`

- Lift `resolveSecretRefs` into the shared import/apply document path so both
  endpoints behave identically; `dryRun` on either validates that every
  reference *resolves* without storing anything (the same
  `ErrUnresolvedSecretRef` → 400).
- Document the two schemes once: `param:` is the SaaS-grade form (org-scoped,
  API-managed); `env:` is the self-hosted form (operator-managed, requires a
  pod restart, warned on SaaS).

### 3. Store the reference, resolve at execution

This is the structural fix for (3) and (4):

- A config string value that contains a reference is stored **as the
  reference** in the public config. The check's effective config is materialized
  at execution time (worker side, or at job build in the API — implementer's
  call, but the resolved value must never be written to `config` or returned by
  any read endpoint).
- `GET /checks/:uid`, the dashboard, and `/checks/export` therefore show
  `password=${param:sso-authtest-password}` — which is exactly what the
  committed file should contain. Round-trip restored with no redaction logic.
- Resolution failure at execution is a check result with `status: error` and
  output `unresolved secret reference: param:…`, so a deleted parameter is
  visible in the check's history rather than silently sending the literal.
- `${env:}` at execution resolves on the *executing* process. For a check
  running on a deported agent that env var lives on the agent, which is a
  feature (per-region secrets) but must be documented; `param:` always resolves
  on the API and travels inside the sealed job payload like other secrets.

### 4. Tests that prove the negatives

- API: org admin creates a secret parameter → `GET` shows no value; a member →
  403; the `sp.` prefix → 400.
- Import/apply parity: the same manifest with `${param:x}` through `/import`
  and `/apply` yields byte-identical stored configs, both containing the
  reference and neither containing the value.
- Execution: an HTTP check whose `body` carries `${param:x}` sends the
  resolved value (httptest server asserts on the received body) while
  `GET /checks/:uid` and `/checks/export` still return the reference string.
- Leak guard: grep the public `config` column after every test in the checks
  package for the fixture secret value — a helper the existing
  `TestNoUndeclaredCheckerSecrets` can host.

### Out of scope

- The UI form dropping `body` on save (`2026-09-11-01`) and the export/import
  round-trip guarantees (`2026-09-11-04`) — both needed for the exp-devops
  workflow, filed separately.

## Implementation Plan

### A. `internal/secretref` — one home for the reference grammar

New package holding what `apply.go` kept private: the `${env:…}` / `${param:…}`
pattern, `Contains`, a `ResolveFunc`-driven `ResolveString` / `ResolveConfig`
(returns a COPY, walks nested maps and slices), the `ErrUnresolved` sentinel and
an `ErrSkip` sentinel a resolver returns to leave a reference *in place*. `ErrSkip`
is what lets the API resolve only `param:` and hand `env:` on to the executing
process. `checks.ErrUnresolvedSecretRef` stays as an alias so the handler's 400
mapping and the existing tests keep working.

### B. Org parameters API

- DB: add `ListOrgParameters(ctx, orgUID)` to `db.Service` + both engines
  (`GetOrgParameter`/`SetOrgParameter`/`DeleteOrgParameter` already exist; the
  `parameters` table needs no schema change, so **no migration**).
- New `internal/handlers/orgparams` (service + handler): `List`, `Get`, `Set`,
  `Delete`. Key regex `^[a-z][a-z0-9_.-]{0,63}$`; the `sp.` prefix is refused
  with `VALIDATION_ERROR`/400 so an org cannot clobber the credentials package's
  DEK store. `secret: true` rows answer `{key, secret, updatedAt}` with **no**
  `value`, in list and in get alike.
- Routes in `app/server.go`, mirroring `orgChecksAdmin`: an
  `orgSlugRedirect → RequireAuth → RequireOrgAccess → RequireOrgAdmin` group at
  `/orgs/:org/parameters`.
- OpenAPI: document the four operations in `openapi/openapi.yaml`.

### C. Resolution moves out of `apply` into the shared document path

`resolveSecretRefs` (mutating) becomes `validateSecretRefs` (non-mutating): it
walks every config string, proves every reference *resolves*, and returns
warnings. Both `ApplyChecks` and `ImportChecks` call it, before any mutation and
on `dryRun` alike, so the two endpoints answer identically. The document is left
holding the reference — that is what gets stored.

### D. Store the reference, resolve at execution

- **Server side** (`param:` only): after a job's secrets are merged
  (`DirectBackend.mergeClaimedSecrets`) and, for agents, before the secret map is
  re-sealed (`agentws.sealSecretsFor`), `param:` references are resolved from the
  org parameter store. On the agent path the resolved value is placed **inside the
  sealed envelope**, where the agent's own merge overrides the reference still
  sitting in the public wire config — so a resolved value never crosses the wire
  in the clear.
- **Executing side** (`env:` and the backstop): `CheckWorker.executeJob`
  materializes the config just before `config.FromMap`, resolving `${env:}` from
  its own process environment. Any reference still unresolved at that point —
  including a `param:` the API could not resolve — is a `saveErrorResult` with
  `unresolved secret reference: param:…`, never a literal sent to the target.
- A private-region agent holding its own key gets a documented limitation:
  the server cannot open its verbatim `config_sealed`, so `param:` inside a
  private-region secret field is not resolvable there; `env:` on the agent is.

### E. CLI, dashboard, docs

- `sp params list|set|delete`.
- Organization → **Parameters** page (list / add / rotate / delete), built from
  the design reference, secret values write-only; locale keys in all four
  locales; a Playwright spec.
- `wiki/features/config-as-code.md` §"Secret references" rewritten to describe
  what is now true: the reference is stored, resolution happens at execution,
  `param:` is the SaaS form and `env:` the self-hosted one.

### F. Tests that prove the negatives

- `orgparams`: admin creates a secret → GET/LIST carry no value; a `user`/`viewer`
  → 403; `sp.` prefix → 400.
- Parity: the same manifest through `/import` and `/apply` stores **byte-identical**
  configs, both holding the reference, neither holding the value.
- Execution: an HTTP check whose `body` carries `${param:x}` reaches an `httptest`
  server with the resolved value while `GET /checks/:uid` and `/checks/export`
  still return the reference.
- Leak guard hosted next to `TestExportNeverCarriesAMintedToken` in
  `internal/checkers/registry/export_leak_test.go`, with a positive control so a
  green run cannot mean "there was nothing to find".
