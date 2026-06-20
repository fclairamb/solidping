# Config-as-code (declarative checks)

> Idea, not ready for implementation. Source: the Maintenant competitor analysis
> ([`../../docs/competitors/maintenant.md`](../../docs/competitors/maintenant.md)) lists
> **Docker-label configuration** — a lightweight config-as-code path — as an advantage SolidPing
> lacks.

## The gap

SolidPing can already move check definitions *as data*:

- **JSON export** of all checks (admin-only) — see the existing idea
  [`2026-01-03-conf-exporters.md`](2026-01-03-conf-exporters.md) and the import/export in
  `server/internal/handlers/checks/`.
- **Network discovery** (CIDR scanning) in `server/internal/discovery/`.
- Related ideas: [`2025-12-28-importers.md`](2025-12-28-importers.md),
  [`2025-12-28-automatic-app-discovery.md`](2025-12-28-automatic-app-discovery.md).

What's missing is a **declarative, idempotent** path: a user keeps a file (YAML/JSON) describing
the checks they want, applies it repeatedly, and SolidPing reconciles to match — the GitOps
story self-hosters and homelabbers expect. Import today is a one-shot operator action, not a
reconcile.

## Sketch

1. **Declarative apply (primary).** `solidping apply checks.yaml` (and/or
   `PUT /api/v1/orgs/{org}/checks:apply`): the file lists checks (+ groups, maybe integrations)
   under a **managed marker** (a label/namespace). Reconcile = create missing, update changed,
   and **delete-by-absence** *only within the managed marker* (never touch hand-created checks).
   Dry-run / diff mode before apply. Secrets referenced by env/SSM, not inlined.
2. **Docker-label discovery (secondary, the Maintenant parity bit).** Read labels off running
   containers (e.g. `solidping.check=http`, `solidping.url=…`) and auto-create the corresponding
   checks. Lower priority than the file-based apply, and overlaps with
   [`2025-12-28-automatic-app-discovery.md`](2025-12-28-automatic-app-discovery.md).

## Open questions

- Scope of the managed surface: checks only, or also check-groups / integrations / status pages?
- Identity for reconcile: match on `slug` within the managed marker? How are renames handled?
- Multi-tenant: per-org files, or one file with org keys (admin only)?
- How do secrets fit (env interpolation? references to `parameters`?) without landing plaintext
  in a committed file — ties into credential encryption (`server/internal/crypto/credentials/`).
- Relationship to the existing JSON export format — is the apply format the same schema,
  round-trippable with export?

## Why it fits SolidPing

It leans on assets that already exist (import/export, discovery, slugs as stable IDs) and serves
the self-hosted/GitOps audience without chasing Maintenant's deep container observability (which
the analysis recommends *not* pursuing).
