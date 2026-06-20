# Config-as-code (declarative checks)

> Active spec — moved from `specs/ideas/` 2026-06-20, **grounded against the codebase**. A few
> items still need a call before building (see *Risks / decisions to confirm*).
> Source: the Maintenant competitor analysis
> ([`../../docs/competitors/maintenant.md`](../../docs/competitors/maintenant.md)) lists
> **Docker-label configuration** — a lightweight config-as-code path — as an advantage SolidPing
> lacks. After reading the code, the real story is smaller and more SolidPing-shaped than
> "build a declarative engine": **most of the machinery already exists; what's missing is the
> reconcile loop.**

## Key insight: this is ~70% built already

SolidPing already moves check definitions *as data*, with a stable on-disk schema and a CLI:

- **Export** — `GET /api/v1/orgs/{org}/checks/export` returns an `ExportDocument`
  (`version`, `exportedAt`, `organization`, `checks[]`, `_secretsStripped`); each `ExportCheck`
  carries `name, slug, type, config, regions, labels, enabled, internal, period, group,
  confirmation/escalation/recovery, dependsOn[]`
  ([`handlers/checks/service.go:1900-1955`](../../server/internal/handlers/checks/service.go)).
- **Import** — `POST /api/v1/orgs/{org}/checks/import` consumes that same document and is already
  **idempotent by `slug`** (`UpsertCheck` per check + additive `dependsOn` merge), returning
  `ImportResult{created, updated, skipped, errors[]}` (same file). **It does not delete.**
- **A CLI already ships** — `sp` (a.k.a. `solidping client`,
  [`server/cmd/sp`](../../server/cmd/sp/main.go)) with `checks list/get/add/update/upsert/remove/
  deps`. There is **no `export`/`import`/`apply`** command yet.
- **Secrets are already enveloped** — checks split into `config` (public JSONB) +
  `config_private` (AES-256-GCM) + `config_private_keys`
  ([`db/models/check.go:60-76`](../../server/internal/db/models/check.go)), keyed by a per-org DEK
  wrapped under `SP_ENCRYPTION_MASTER_KEY`
  ([`crypto/credentials/service.go`](../../server/internal/crypto/credentials/service.go)). Export
  **strips** secret keys (`_secretsStripped: true`), so a committed file is already secret-free.
- **`labels map[string]string`** already exist on checks and round-trip through export — a ready
  home for a "managed-by" marker without any new column.

So "config-as-code" for SolidPing is **not** a green-field engine. It is: *close the loop on the
export format that already ships* — add reconcile (delete-by-absence within a managed scope),
dry-run/diff, secret references, and an `sp apply` command.

## The actual gap (what GitOps needs that we don't have)

1. **Reconcile, not just upsert.** Import creates/updates but never deletes; re-applying a file
   from which you removed a check leaves the check running. True config-as-code needs
   **delete-by-absence** — but *only* within a clearly bounded, opted-in scope (never touch
   hand-created checks).
2. **A managed scope marker.** Today all checks are equal — there is no "owned by a manifest"
   concept, so delete-by-absence would be unsafe. Need a reserved marker to fence the managed set.
3. **Dry-run / diff.** No way to preview create/update/**delete** before applying. Mandatory for a
   destructive operation.
4. **Secret references.** Export strips secrets, so an applied manifest has *no* way to supply the
   token/header a check needs — it would silently downgrade auth on every apply. Need a
   **reference** convention (resolved server-side), never inlined plaintext.
5. **No `apply` surface / CLI verb.** The reconcile operation needs an endpoint and an `sp apply`
   command (the CLI is the natural front door for the self-hosted/GitOps audience).
6. **Authorization gap (latent).** Export/import today are gated by `RequireAuth` **only** — any
   org member can run them ([`app/server.go:551-554`](../../server/internal/app/server.go)),
   despite [`2026-01-03-conf-exporters.md`](../ideas/2026-01-03-conf-exporters.md) intending admin-only. A
   *destructive* apply must be **admin-gated** (the discovery handler's `isAdmin()` is the
   precedent), and we should gate export/import the same way while we're here.

## Design that fits SolidPing

### The manifest = the existing export document + two small additions

Reuse `ExportDocument`/`ExportCheck` verbatim (so `sp checks export` bootstraps a manifest and the
loop round-trips). Add only:

- **A managed marker as a reserved label**, e.g. `solidping.io/managed: <manifest-name>` on every
  check the manifest owns. Because labels already round-trip, this needs no schema change. The
  reconcile scope is exactly "checks in this org carrying `solidping.io/managed=<name>`."
- **Secret references** inside `config` values: `${env:NAME}` and `${param:KEY}` (the `parameters`
  table — org-scoped or system-wide, already supports `secret=true`). Resolved **server-side at
  apply time** into the encrypted envelope; the literal `${…}` is what lives in the committed file.
  A missing reference is an apply error, not a silent blank.

### The apply operation (admin-only)

`POST /api/v1/orgs/{org}/checks:apply` (and `sp apply [-f] checks.yaml`):

1. Parse JSON **or** YAML into the existing `ExportDocument` struct (YAML is the GitOps-friendly
   surface; JSON is what export already emits — accept both).
2. Resolve `${env:…}` / `${param:…}` references; fail closed on any unresolved reference.
3. Compute a **plan** against the managed scope: `create` (in file, absent in org), `update`
   (slug exists — diff config/fields), `delete` (carries the managed label, absent from file),
   `unmanaged` (matches a slug *without* the managed label → reported, never auto-adopted/deleted).
4. **`?dryRun=true` returns the plan only** (the diff), changing nothing. `sp apply` prints it and
   prompts unless `--yes`.
5. On apply: run create/update via the existing upsert path; perform deletes **only** for managed,
   absent checks; stamp/refresh the managed label on every owned check. Return an extended
   `ImportResult` that also reports `deleted` and the plan.

Safety rails: admin-only; dry-run-by-default in the CLI; a configurable **deletion cap** (refuse if
a single apply would delete more than N managed checks unless `--force`) so a bad file can't wipe a
fleet.

### Identity & renames

Match on **`slug`** within the managed scope (the existing import key; `checks_slug_idx` is unique
per org). A `uid` is immutable but absent from hand-written manifests, so slug is the practical key.
Renaming a slug reads as delete+create (a standard GitOps caveat) — mitigate with an **optional
`previousSlug`** (or explicit `uid`) on a check so a rename reconciles in place instead of
recreating. Document the default behavior either way.

## Open questions — resolved against SolidPing

| Original open question | Resolution (leaning) |
|---|---|
| Scope: checks only, or groups/integrations/status pages? | **Checks first** — plus the groups/deps/labels the export schema *already* carries. Integrations and status pages are a later phase; keep the manifest a strict superset of today's export. |
| Identity for reconcile / renames | **Match on `slug`** within the managed label scope. Renames via optional `previousSlug`/`uid`; otherwise rename = delete+create (documented). |
| Multi-tenant: per-org or one file with org keys? | **Per-org**, mirroring the export/import endpoints (`/orgs/{org}`). One manifest per org; **admin-only**. Cross-org/system-wide is out of scope. |
| How do secrets fit without plaintext in git? | **References, never inlines**: `${env:NAME}` / `${param:KEY}` resolved server-side into the existing encrypted envelope. The committed file stays secret-free, consistent with `_secretsStripped` export. |
| Relationship to the export format | **Same schema** (`ExportDocument`) + a managed-label marker + secret-ref convention, so `export → edit → apply` round-trips. Apply is the reconcile sibling of import. |

## Phasing

- **Phase 1 — Apply on the existing format (checks, no deletes).** `:apply` endpoint + `sp apply`,
  dry-run/diff, admin-gating (and retro-gate export/import). Behaves like a previewable, idempotent
  import. Low risk, immediately useful.
- **Phase 2 — Managed scope + delete-by-absence + secret references.** The reserved label, the
  deletion cap, `${env}`/`${param}` resolution. This is where it becomes true GitOps.
- **Phase 3 (optional parity) — Docker-label discovery.** Read `solidping.*` labels off running
  containers and *suggest* checks — but this overlaps
  [`2025-12-28-automatic-app-discovery.md`](../ideas/2025-12-28-automatic-app-discovery.md) and the existing
  CIDR discovery ([`server/internal/discovery/`](../../server/internal/discovery/)), which are
  **suggest-not-declarative** by design. Treat it as a thin discovery source feeding the same apply
  path, not a second reconcile engine. Lowest priority; the analysis explicitly recommends **not**
  chasing Maintenant's deeper container observability.

## Risks / decisions to confirm before building

- **Destructive surface.** Delete-by-absence on monitoring config is high-blast-radius. The managed
  label + dry-run-default + deletion cap + admin-only are the guardrails; confirm they're enough,
  or add an explicit `prune: true` flag required to enable any deletion.
- **Retro-gating export/import to admin** may break anyone scripting them as a normal user today —
  a deliberate (small) back-comat decision, worth a release note.
- **YAML support** adds a dependency and a second parse path; confirm we want YAML *and* JSON vs.
  JSON-only (export already emits JSON; YAML is purely ergonomics for hand-authored manifests).
- **Plaintext fallback.** When `SP_ENCRYPTION_MASTER_KEY` is unset, resolved secrets land in
  plaintext `config` — apply should *warn* (or refuse to resolve `${…}`) so a self-hoster doesn't
  unknowingly commit a setup that stores secrets unencrypted.

## Why it fits SolidPing goals

- **Self-hosted / GitOps audience.** A previewable, idempotent `sp apply checks.yaml` is exactly
  the workflow homelabbers and self-hosters expect, and it directly answers the
  "what if the service disappears — I want my config in git" motivation behind
  [`2026-01-03-conf-exporters.md`](../ideas/2026-01-03-conf-exporters.md).
- **Leans entirely on assets that already exist** — the export schema, the idempotent
  upsert-by-slug import, the `sp` CLI, the secret envelope + `parameters` table, and check labels.
  Phase 1 is mostly *wiring and guardrails*, not new subsystems.
- **Stays in lane.** It serves the declarative-config niche without chasing Maintenant's deep
  container observability (live CPU/mem/log streaming), which the competitor analysis recommends
  against.
