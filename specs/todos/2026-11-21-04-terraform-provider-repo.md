---
model: sonnet
effort: high
---

# Publish `terraform-provider-solidping`: audit the maintenance-window API here, then build and release the provider in its own repo

GitHub Issue: https://github.com/fclairamb/solidping/issues/965

## Source

Florent split Part A out of `specs/done/2026/11/2026-11-21-01-terraform-provider-and-wider-apply.md`
(issue #958): the Terraform/OpenTofu provider itself, in a new repo, published to
registry.terraform.io and the OpenTofu registry. Part B (`sp apply` for status pages,
channels, maintenance windows) ships in this repo. The issue sets the provider's shape and
says Florent creates the repo, the GPG key and the registry accounts first.

## Problem

- `fclairamb/terraform-provider-solidping` does not exist yet (`gh repo view` → not found,
  2026-11-21), nor the GPG key in gopass (`personal/solidping/terraform-provider/gpg-*`).
  Nothing of Part A can start in its repo before that.
- `wiki/terraform-provider-api-audit.md` audits checks, channels, status pages, escalation
  policies and on-call schedules, but has no row for maintenance windows
  (`handlers/maintenancewindows/`, routes `orgs/:org/maintenance-windows`,
  `server/internal/app/server.go:2264`), which v1 needs.
- The shape is fixed by `specs/done/2026/05/2026-05-25-10-terraform-provider.md`.

## Why a spec

- Out of this tree (a new repo, release pipeline, two registries) and blocked on operator
  setup: far beyond a direct fix.
- May surface API gaps (maintenance-window read-back, `PUT /:uid/checks`) that are new or
  changed endpoints.

## Proposal

### Step 1, in this repo (can run now)

Extend `wiki/terraform-provider-api-audit.md` with a `solidping_maintenance_window` section,
same checklist as the others: create returns the full object, GET by uid read-back fidelity
(recurrence fields included), PATCH partial, `check_uids` set through `PUT /:uid/checks`,
DELETE → 404 when gone, import by `org/uid`. Ground each row in `file:line`. File every gap
as a small follow-up spec in `specs/todos/` (do not fix it here).

### Step 2, in `fclairamb/terraform-provider-solidping` (after the prerequisites)

- Terraform Plugin Framework, handwritten thin client. Provider config `endpoint` / `org` /
  `token` with `SOLIDPING_ENDPOINT` / `SOLIDPING_ORG` / `SOLIDPING_TOKEN` fallbacks.
  Resource `id` = uid, `terraform import org/uid`, `404` on read → removed from state,
  `{title, code, detail}` → diagnostics, secret config keys `sensitive` and write-only so
  read-back placeholders never diff.
- v1 resources: `solidping_check`, `solidping_channel` (non-Slack; Slack stays install-flow
  only, documented), `solidping_status_page` (sections as `solidping_status_page_section` if
  time allows, else v1.1), `solidping_maintenance_window` (with `check_uids`). Data sources
  by uid or slug for checks, channels, status pages. Escalation policies and on-call
  schedules in v1.1.
- Release: GoReleaser + HashiCorp's scaffolding-framework release workflow (GPG-signed
  `SHA256SUMS`), registry.terraform.io, and an `opentofu/registry` PR with the provider and
  its GPG key.
- Acceptance tests (`TF_ACC=1`) against SolidPing started in CI from the published Docker
  image: create, update, import, second `plan` empty, destroy, per resource.
- Link the provider from the SolidPing docs once published.

## Open questions

1. **Prerequisites.** Florent creates `fclairamb/terraform-provider-solidping`, generates the
   GPG key in gopass, adds it to the repo secrets and to both registries. Recommended: run
   Step 1 now and stop there until Florent confirms the setup; `/implement-todos` keeps the
   spec in `specs/todos/` with Step 2 pending. Trade-off: the issue stays tracked here while
   the provider work happens elsewhere.

## Closing the issue

This spec closes #965. The PR that completes Step 2 (or, if Step 2 lands only in the
provider repo, the PR archiving this spec) **must** carry a `Closes #965` line. When the spec
is archived to `specs/done/`, verify the issue is closed and, if not, close it by hand:

    gh issue close 965 --comment "Implemented by <PR or commit>; spec: <archived spec path>"
