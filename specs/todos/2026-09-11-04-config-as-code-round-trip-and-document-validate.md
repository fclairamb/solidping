---
model: opus
effort: high
---

# A fresh export doesn't round-trip: no document-level validate endpoint, dry-run reports 482 updates for a no-op, and every external validator drifts from the server

## Problem

From the `#solidping-dev` thread of 2026-09-11
(https://stonaltech.slack.com/archives/C0BFC2XG4K0/p1789139320451959?thread_ts=1789138055.738169&cid=C0BFC2XG4K0),
on the tracked config of the `stonal` org (exp-devops `solidping/config.yaml`):

> « la config dans git elle est hors sol … et en plus elle valide pas du tout …
> j'ai 200 erreurs alors que c'est juste la conf exportée de base »

and the product position stated in reply:

> « ça devrait être directement géré par l'API … on file du YAML ou du JSON en
> mode dry-run et puis le serveur dit ok … j'aimerais autant que possible
> permettre de gérer ça via du terraform au plus simple »

What actually happened, and why each step is a product gap rather than a user
error:

1. **The org's own export failed the org's validator with 197 problems.** 183
   were regions: the server stores and exports private locations in the folded
   form `@aws-paris`, while the documented/accepted long form is
   `@stonaltech/aws-paris`. The server's own
   [`regionRegex`](server/internal/handlers/checks/validate_document.go:38)
   accepts both; nothing tells a third party that the short form is what comes
   back. 12 more were check types (`domain`, `oracle`, `smtp`, `email`) the
   external validator predated. An external validator *must* drift: it
   re-implements what only the server knows.

2. **There is no document-level validate endpoint.** `POST /checks/validate`
   ([`server.go:1063`](server/internal/app/server.go:1063)) validates a single
   check. `ValidateDocument`
   ([`validate_document.go`](server/internal/handlers/checks/validate_document.go))
   exists — generic rules, all issues, no I/O — but is reachable only inside
   `import`/`apply`, which are **admin-only**
   ([`server.go:1053-1055`](server/internal/app/server.go:1053)). A CI job that
   wants "is this file valid?" without a write-capable token cannot ask.

3. **Dry-run cannot prove a no-op.** `import --dry-run` on a file that is
   byte-for-byte the current export answers `created=1 updated=482 skipped=0`
   ([`ImportChecks`](server/internal/handlers/checks/service.go:3507)): every
   matched slug counts as `updated` whether or not anything would change. The
   one question config-as-code exists to answer — *does the file match the
   instance?* — needs a client-side diff, which is what `solidping_config.py
   diff` and `sp checks diff` had to build.

4. **An export can be invalid.** An empty `name` is accepted on write, omitted
   by the exporter, required by the importer (`domain-stonal-dev-io`, same
   day). Filed with its fix in `2026-09-11-02`; listed here because the
   round-trip test below is what catches the *class*.

5. **The CLI that would fix (1)–(2) is not obtainable.** `sp checks validate
   <file>` exists (spec `2026-08-05-03`) and runs the server's own
   `ValidateDocument` offline — but release `v0.27.1` publishes **zero
   assets**, so a GitHub Actions job cannot download it. Third parties keep
   their Python validators because that is what they can run.

## Proposal

### 1. `POST /api/v1/orgs/:org/checks/validate` accepts a whole document

- Same route, content-negotiated: a body with a top-level `checks` list (JSON or
  YAML, via `ParseManifest`) is a document; anything else keeps today's
  single-check behaviour. **Member-level** (read) authorization: validating
  mutates nothing and needs no admin token.
- Response: `{ valid, issues: [{ slug, field, code, message }], plan? }` — every
  issue, never first-error-only (the single-check path's `formatValidateError`
  legacy must not leak in). `code` is stable (`REGION_FORMAT`,
  `UNKNOWN_TYPE`, `INLINED_CREDENTIAL`, `UNRESOLVED_SECRET_REF`, …) so a CI
  job can allow-list.
- With `?plan=true` (admin), also return the reconcile plan from (2).

### 2. Dry-run reports `unchanged`, per check, with a field diff

- `import`/`apply` dry-run: each check gets an action in
  `create | update | unchanged | delete | unmanaged`; the response counts all
  five; `update` entries carry `changes: [{ field, from, to }]` with secret
  and reference-derived values masked.
- "Unchanged" is computed on the **normalized effective** config
  (`normalizeCheckConfig` + region folding + defaults resolution), so the
  folded/long region spelling, `expected_status` vs `expectedStatusCodes` and
  document defaults never count as change. This is the definition of
  round-trip.
- `created=0 updated=0 deleted=0` with N `unchanged` is then the machine-
  readable "the file matches" — no client diff needed. `sp checks diff` and the
  Python `diff` become presentation over this response.

### 3. The round-trip guarantee, as a test

For every checker's sample config (the registry already has them) and for a
fixture org with defaults, private locations, groups, labels and
dependencies:

```
export → validate-document = 0 issues
export → import(dryRun)   = 0 create / 0 update / N unchanged
export → apply(dryRun)    = same, 0 unmanaged
```

plus the same three after `export → export` (idempotence). This test is the
contract external tools can rely on; today it fails on (1), (3) and (4).

### 4. Ship the CLI so CI can run it

- Release assets for `sp` (darwin/linux × amd64/arm64) on every tag, plus a
  `ghcr.io/…/sp` image — whichever the existing release-please pipeline can
  attach with the least ceremony. Document `sp checks validate config.yaml` as
  *the* validator; the exp-devops Python one keeps only the org-specific
  rules (stack roots, RabbitMQ consistency) that are genuinely not the server's
  business.

### 5. Say what the canonical spellings are

- `wiki/features/config-as-code.md` and the docs site: the folded `@location`
  form is canonical on export; the `@org/location` form is accepted on input.
  `expectedStatusCodes` over `expected_status`. The `secrets: stripped` marker's
  exact guarantee (after `2026-09-11-02`).

### Terraform

Not a deliverable here. (1) + (2) are precisely the two calls a provider needs
— *plan* and *validate* — so this spec is the prerequisite, and the Terraform
provider is a follow-up spec once these exist.

---

## Implementation Plan

### 0. What the three sibling specs already landed (not redone here)

- `-02` — `name` is refused blank on write and falls back to the slug; the
  exporter strips `SecretFields() ∪ ExportRedactedFields()`;
  `export_roundtrip_test.go` already runs `export → ValidateDocument →
  import(dryRun)` over every sample config. **Extended, never duplicated.**
- `-03` — secret references are stored verbatim and resolved at execution;
  `/import` and `/apply` judge them identically through `validateSecretRefs`
  → `secretref.ResolveConfig`; an unresolvable one is a hard 400.
- `-01` — frontend only.

### 1. `POST /api/v1/orgs/:org/checks/validate` accepts a whole document

- **Route moves from `orgGroup` (write floor) to `orgGroupSelf` (read)**, so a
  read-only member / CI token can validate. The single-check path keeps the
  write floor through an **inline** role gate in the handler that emits
  `middleware.ViewerWriteMessage` verbatim — which is what keeps
  `TestEveryOrgScopedWriteRouteRefusesViewers` green without an allowlist
  entry (its probe body is `{}`, i.e. the single-check path). `/import` and
  `/apply` are not touched.
- The handler reads the body once and sniffs it: a top-level `checks` list
  (JSON **or** YAML, via `ParseManifest`) is a document; anything else decodes
  as today's `ValidateCheckRequest`.
- New `ValidateDocumentResponse{valid, issues[], plan?}`; `DocumentIssue` grows
  `Field` and a stable `Code` and serializes as `{slug, field, code, message}`.
- Codes: `UNSUPPORTED_VERSION`, `MISSING_ORGANIZATION`, `INVALID_SECRETS_MARKER`,
  `EMPTY_CHECKS`, `MISSING_FIELD`, `INVALID_SLUG`, `DUPLICATE_SLUG`,
  `INTERNAL_NOT_WRITABLE`, `UNKNOWN_TYPE`, `INVALID_CONFIG`,
  `INLINED_CREDENTIAL`, `STATUS_FIELD_CONFLICT`, `INVALID_PERIOD`,
  `INVALID_LABEL`, `REGION_FORMAT`, `INVALID_DEPENDS_ON`, `DEPENDENCY_CYCLE`,
  `UNRESOLVED_SECRET_REF`.
- `UNRESOLVED_SECRET_REF` is the org-aware half: `ValidateDocument` is offline
  and cannot resolve, so the endpoint additionally runs a **per-check**
  collecting variant of `validateSecretRefs` (the write path keeps its
  first-error contract by reading the same list).
- `?plan=true` requires **admin** (same inline role gate, `MemberRoleAdmin`)
  and returns the dry-run `ApplyResult` from (2).

### 2. `unchanged`, per check, with a field diff

- New `server/internal/handlers/checks/diff.go`:
  `CheckFieldChange{field, from, to}` and the differ.
- The export per-check projection is **extracted** from `ExportChecks` into
  `buildExportCheck`/`loadExportProjection`, and a new `orgCheckSnapshot`
  reuses it to project the org's current state into the *same* `ExportCheck`
  shape a document carries. Round-trip is then true by construction rather
  than by two implementations agreeing.
- Desired side is normalized the way the write path normalizes:
  `normalizeCheckConfig`, redacted-field preservation/derivation, the export
  stripper, `ResolveRegionsForCheck` (folds `@org/loc` → `@loc`), period →
  seconds, JSON round-trip so YAML ints and JSON floats compare equal.
- Diff respects the *write* semantics, so a no-op document is a no-op:
  - config: non-secret keys replace wholesale (both directions), secret and
    export-redacted keys are stripped from both sides — a document that
    *supplies* one is reported as a **masked** change, since a dry run cannot
    prove equality against an encrypted column;
  - regions/group: only compared when the document names them (the upsert
    leaves them alone otherwise);
  - `escalationThreshold` is excluded — `UpsertCheckRequest` does not carry it;
  - `dependsOn` is additive (pass 2 merges), so only *missing* edges count;
  - `solidping-managed` is excluded from the label diff (apply stamps it).
- `ImportResult` gains `Unchanged`, `Deleted`, `Unmanaged` and a
  `Plan []ImportPlanEntry{slug, action, changes}`; `ApplyResult` gains
  `Unchanged` and `ApplyPlanEntry.Changes`, plus the `unchanged` action.
- Applied to **real runs too**, not only dry runs: the snapshot is read before
  any mutation, so the action is the honest pre-state either way.
- `sp checks diff` becomes presentation over the plan: it asks the server for
  the apply dry run, prints the per-check actions and field changes, and keeps
  the 0/1/≥2 exit contract; the old text diff stays as the fallback when the
  caller cannot plan (non-admin) and behind `--text`.

### 3. Round-trip guarantee, as a test

Extends `export_roundtrip_test.go` with a fixture org that carries a private
location, a group, labels, dependencies, org default regions and the folded /
long region spellings:

```
export → validate-document = 0 issues
export → import(dryRun)    = 0 create / 0 update / N unchanged
export → apply(dryRun)     = same, 0 unmanaged
export → export            = the same three, byte-identical minus exportedAt
```

The two filters `-02` installed are re-examined: the `INLINED_CREDENTIAL` hint
filter must stay (it is a *hint* on any key containing `user`/`pass`/…, and a
plain `username` is not a credential), the stripped-declared-secret filter must
stay (the checker's offline `Validate` cannot know the operator supplies it at
import). Both are reported as such rather than silently kept.

### 4. Ship the CLI

- A `cli-release` job in `.github/workflows/ci.yml`, tag-triggered like
  `docker`: cross-compiles `sp` for darwin/linux × amd64/arm64, uploads the
  archives to the GitHub release with `gh release upload --clobber`, and
  builds/pushes `ghcr.io/<repo>/sp` from a small `Dockerfile.sp`.
- **No release is run and no tag is pushed.** Verified by building all four
  binaries locally and by a YAML parse of the workflow.
- `sp checks validate config.yaml` documented as *the* validator.

### 5. Canonical spellings

`wiki/features/config-as-code.md` + a new `web/docs/docs/features/config-as-code.md`
state: folded `@location` is canonical on export and `@org/location` is accepted
on input; `expectedStatusCodes` supersedes `expected_status`; and what
`secrets: stripped` guarantees after `-02` (`SecretFields() ∪
ExportRedactedFields()`, restored on import). `wiki/api-specification/checks.md`
and `openapi.yaml` carry the endpoint contract and the code list.
