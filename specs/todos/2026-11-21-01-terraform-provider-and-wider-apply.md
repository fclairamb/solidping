---
model: sonnet
effort: high
---

# Ship the Terraform/OpenTofu provider and widen `sp apply` past checks

GitHub Issue: https://github.com/fclairamb/solidping/issues/958

## Source

Feedback from a self-hoster on the feddit.org `c/foss_de` post about SolidPing (2026-11-19, in
German, filed by Florent): they manage everything as IaC, want "an extensive API, a public
status page with incidents and maintenance, notifications via RSS, ntfy, Matrix", and went to
phare.io because nothing self-hosted ticked every box.

Where the report is right: there is no Terraform/OpenTofu provider. Where it is wrong (for
SolidPing): everything else on the list ships already (REST API + OpenAPI, `sp` CLI, MCP;
public status pages with incidents, maintenance badge and Atom feed; ntfy and Matrix
channels; `sp checks export/validate/diff` and `sp apply` for checks). The ask is therefore
two things: publish `terraform-provider-solidping` (checks, channels, status pages,
maintenance windows at minimum) on the Terraform and OpenTofu registries, and let `sp apply`
manage more than checks, for people who prefer YAML over HCL.

## Problem

1. **The provider repo does not exist.** `gh repo view fclairamb/terraform-provider-solidping`
   fails. The 2026-05-25 spec (`specs/done/2026/05/2026-05-25-10-terraform-provider.md`) only
   delivered the in-tree API audit (`wiki/terraform-provider-api-audit.md`) and its follow-up
   (`specs/done/2026/05/2026-05-25-14-uid-addressing-escalation-oncall.md`, uid addressing for
   escalation policies and on-call schedules). Plan steps 2 to 4 (bootstrap, resources,
   acceptance tests, registry publish) were explicitly out of tree and never started.
2. **The audit does not cover maintenance windows**, which the issue lists. Their API is
   complete CRUD by uid (`server/internal/app/server.go:2249-2256`: `GET/POST ""`,
   `GET/PATCH/DELETE /:uid`, `GET/PUT /:uid/checks` for the attached checks), but the model
   (`server/internal/db/models/maintenance_window.go:10-22`) has no slug and no labels: only
   `uid`, `title`, `description`, `startAt`, `endAt`, `recurrence`, `recurrenceEnd`. Fine for
   Terraform (state holds the uid), not for a YAML manifest, which needs a stable key to match
   a manifest entry to a row and an ownership marker for prune.
3. **`sp apply` is checks-only.** `server/pkg/cli/apply.go` posts to
   `POST /api/v1/orgs/:org/checks/apply` (`server/internal/app/server.go:1300`), backed by
   `checks.Service.ApplyChecks` (`server/internal/handlers/checks/apply.go:362`), which takes an
   `ExportDocument` of checks only (`len(doc.Checks) == 0` → `ErrEmptyChecksArray`, line ~373).
   Ownership is the `solidping-managed=<manifest>` label (`apply.go:36`), prune is
   delete-by-absence among labelled rows with a deletion cap (`apply.go:62`, `pruneTargets` at
   `apply.go:510`). Status pages (slug, `models/status_page.go:82`), channels
   (`models/integration.go:130`, `name` but no slug, no labels) and maintenance windows (no key
   at all) have no equivalent.

## Why a spec

- A new or changed API endpoint and OpenAPI change: apply endpoints for status pages, channels
  and maintenance windows (or one multi-kind apply).
- A DB migration: a stable manifest key and an ownership marker for channels and maintenance
  windows.
- Far over ~4 files / ~150 lines, and a whole new repository with registry publishing.
- Data deletion: prune on three more resource kinds.
- A product decision the reporter didn't settle (see Open questions).

## Proposal

### Part A: moved to #965

The provider repo, its release and the registry listings are tracked in #965 (operator setup needed). Nothing to do for Part A here.

### Part B: `sp apply` for status pages, channels and maintenance windows (this repo)

- **Manifest**: the existing export document gains optional top-level `statusPages`,
  `channels`, `maintenanceWindows` arrays next to `checks` (same `version`). A manifest with
  only non-check kinds is valid: replace the `ErrEmptyChecksArray` rule with "at least one
  kind is non-empty". Cross-references by slug/key (a maintenance window's `checks: [slug…]`,
  a status page section's check slugs), resolved after the checks of the same manifest.
- **Identity and ownership**: status pages match on `slug`. Channels and maintenance windows
  get a nullable, per-org-unique `slug` column (migration, Postgres + SQLite), set by apply and
  optional in the API/UI; rows without one are never touched by apply. Ownership uses a
  `managed_by` column (manifest name) on all three tables rather than labels, since only checks
  have labels; prune only deletes rows whose `managed_by` equals the manifest, under the same
  deletion cap as checks.
- **Server**: generalise `ApplyChecks` into an orchestrator that plans and applies each kind in
  dependency order (checks, channels, maintenance windows, status pages; prune in reverse),
  reusing each service's create/update/delete. Same endpoint (`POST /checks/apply` keeps working)
  plus a kind-neutral alias `POST /orgs/:org/apply`; admin-only like today
  (`server.go:1275-1300`). Plan entries carry a `kind`. Secrets in channel settings follow the
  `${env:…}` reference rules checks already use.
- **CLI**: `sp apply` prints the plan grouped by kind; `sp export` (or `sp checks export
  --all-kinds`) emits all four kinds so a user can bootstrap a manifest from a live org.
- **Docs and OpenAPI**: update the config-as-code docs and the OpenAPI document for the new
  fields, endpoint and plan shape.
- **Tests**: per kind, the cases `checks/apply_test.go` covers today (create/update/unmanaged,
  dry run mutates nothing, managed marker stamped, delete-by-absence requires prune, prune never
  touches unmanaged, deletion cap, secret refs stored as references, idempotent second apply),
  plus a cross-kind manifest (maintenance window referencing a check created in the same
  apply), the migration on both backends, and the integration authz sweep
  (`checks_validate_document_authz_test.go`) extended to the new route.

## Resolved open questions

1. **Order of delivery.** Ship Part B first, in this repo; Part A follows in its own repo (#965). (autopilot: recommended answer)
2. **Who creates and signs the provider release.** Florent creates the repo and the GPG key; tracked in #965, out of scope here. (autopilot: recommended answer)
3. **Identity for channels and maintenance windows.** Add a new optional, per-org-unique `slug` column. (autopilot: recommended answer)

## Closing the issue

This spec closes #958. The issue was closed on conversion, so the implementing PR body cites it
as `Refs #958` (or `Closes #958`, harmless on a closed issue). When the spec is archived to
`specs/done/`, verify #958 is closed and, if not, close it by hand:

    gh issue close 958 --comment "Implemented by <PR or commit>; spec: <archived spec path>"
