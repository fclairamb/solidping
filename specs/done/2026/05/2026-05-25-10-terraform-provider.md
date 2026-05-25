# Terraform Provider (Configuration as Code)

> Roadmap: **P2.2** (`docs/roadmap.md`). Gatus, Checkly, BetterStack, Pingdom all ship
> one. DevOps teams that manage infra as code won't adopt a tool that doesn't fit that
> workflow. No provider exists today.

## Context

A `terraform-provider-solidping` lets users declare checks, channels, escalation
policies, on-call schedules, and status pages as Terraform resources. Because the v1 REST
API is already complete, stable, and well-tested, the provider is mostly schema mapping
over an HTTP client — lower effort than it appears.

**Lives in a separate repo** (`terraform-provider-solidping`), out of this tree, so it
can be released to the Terraform Registry on its own cadence. This spec defines its shape
and the **in-tree prerequisite work**; the provider implementation happens in that repo.

### What this repo already provides

- Org-scoped **Personal Access Tokens**: `POST /api/v1/orgs/:org/tokens`
  (`server.go:375`, `authHandler.CreateToken`), listable/revocable
  (`server.go:354,358,373-375`). This is the provider's auth mechanism — no new auth work.
- Full CRUD handlers for every v1 resource (verified present):
  `server/internal/handlers/{checks,channels,escalationpolicies,oncallschedules,statuspages}/`.
- API conventions documented in `docs/api-specification.md` and root `CLAUDE.md`:
  `data` envelope on lists, `$uid` paths, camelCase, `PATCH` for updates.

## Goals

1. Manage the core resources declaratively with full CRUD + `terraform import`.
2. Authenticate with an org-scoped PAT, matching the API today.
3. Map cleanly onto existing endpoints — surfacing any field the API doesn't expose as a
   **small follow-up API task in this repo**, not a blocker.

## Non-goals

- Generating the provider from OpenAPI automatically (there is no committed OpenAPI YAML
  to generate from — `docs/api-specification.md` is prose). Handwrite the thin client.
- Managing org/user/auth-provider lifecycle via Terraform (out of scope for v1).
- Data-source coverage beyond the five v1 resources.

## Resources (v1)

| Resource | Backing handler | CRUD verbs |
|---|---|---|
| `solidping_check` | `handlers/checks/` | POST / GET / PATCH / DELETE |
| `solidping_channel` | `handlers/channels/` | POST / GET / PATCH / DELETE |
| `solidping_escalation_policy` | `handlers/escalationpolicies/` | POST / GET / PATCH / DELETE |
| `solidping_oncall_schedule` | `handlers/oncallschedules/` | POST / GET / PATCH / DELETE |
| `solidping_status_page` | `handlers/statuspages/` | POST / GET / PATCH / DELETE |

Read-only **data sources** for the same five entities support referencing existing
objects by `uid` or slug.

> **Caveat — Slack channels.** Per `specs/todos/2026-05-25-04-fix-slack-channel-install-only.md`,
> Slack channels can only originate from the OAuth install flow and `POST /channels` with
> `type=slack` is rejected. `solidping_channel` must document this: Slack channels are not
> manageable via Terraform; the resource covers webhook/email/discord/etc. types only.

## Provider config

```hcl
provider "solidping" {
  endpoint = "https://solidping.example.com"  # base_url
  org      = "default"                          # org slug
  token    = var.solidping_pat                  # org-scoped PAT, sensitive
}
```

`token` reads from `SOLIDPING_TOKEN` env var as a fallback (Terraform convention for
sensitive credentials).

## Implementation notes (in the separate repo)

- Built with the **Terraform Plugin Framework** (`terraform-plugin-framework`), Go.
- Thin handwritten HTTP client over the v1 API — honor the `data` envelope, `$uid` paths,
  camelCase, and PATCH semantics. PATCH maps naturally onto Terraform's partial updates.
- Resource `id` = entity `uid`; `terraform import` accepts `org/uid`.
- Map the API error shape (`{title, code, detail}`) to Terraform diagnostics; treat 404
  on read as "resource gone" (triggers recreate), `VALIDATION_ERROR` as a plan-time error.
- Publish to the Terraform Registry; document a minimal example per resource.

## In-tree prerequisite: API completeness audit

Before/while the provider repo is built, confirm each resource's API exposes everything
a declarative lifecycle needs. For each of the five handlers, verify:

1. **Create returns the full object** (incl. server-assigned `uid`) so the provider can
   set state without a follow-up GET.
2. **GET by uid** returns every field that Create accepts (no write-only fields that
   can't be read back → would cause perpetual diffs). Secret-bearing fields (per the
   credential-encryption work) are returned as `configPrivateKeys: [...]` placeholders,
   not values — the provider must treat these as write-only / `sensitive` and not diff on
   them. Document this per affected resource.
3. **PATCH** accepts partial updates and preserves omitted secret keys (matches the
   documented PATCH semantics).
4. **DELETE** is idempotent enough (404 on already-deleted is fine).

File any gap found as a small follow-up `specs/todos/` API task in **this** repo, named
per the spec convention; do not block the provider on large API changes.

## Verification

This spec's in-tree deliverable is the **completeness audit** (a short findings note +
any follow-up task files). The provider itself is verified in its own repo:

- `terraform plan` / `apply` against a dev SolidPing (e.g. the laptop tunnel
  `solidping.k8xp.com`) creates each resource type; `terraform import org/uid` adopts an
  existing one; a second `plan` shows **no diff** (proves read-back fidelity, esp. for
  secret placeholders).
- Acceptance tests (`TF_ACC=1`) in the provider repo cover create/read/update/delete +
  import per resource.

## Implementation plan

1. **API completeness audit** (in this repo) — walk the five handlers per the checklist
   above; write findings; create follow-up API task specs for any gaps (e.g. a field not
   returned by GET). This is the only code-touching work that may land here.
2. **Provider repo bootstrap** (separate repo `terraform-provider-solidping`) — scaffold
   with `terraform-plugin-framework`, provider config (endpoint/org/token), HTTP client
   honoring the `data` envelope + error shape.
3. **Resources + data sources** — implement the five resources with CRUD + import and the
   five read-only data sources; mark secret fields `sensitive`/write-only; document the
   Slack-channel caveat.
4. **Acceptance tests + docs** — `TF_ACC` tests per resource; a minimal example per
   resource; Registry publish workflow.
5. **Archive** — move this spec to
   `specs/done/2026/05/2026-05-25-10-terraform-provider.md` once the audit is done and the
   provider repo is bootstrapped.

## Priority

P2.2. Depends on the stable v1 REST API (done). Mostly out-of-tree; the only in-repo work
is the completeness audit and any follow-up API tasks it surfaces.

## Implementation Plan

The in-tree deliverable for this spec is the **API completeness audit** plus follow-up
task files for any gaps. The provider itself is built out-of-tree (separate repo) and is
explicitly out of scope here (Non-goals + Implementation notes).

1. **Walk the five resource handlers against the audit checklist** — for each of
   `checks`, `channels`, `escalation_policy`, `oncall_schedule`, `status_page`, confirm:
   (a) Create returns the full server-assigned object, (b) GET-by-uid returns every
   writable field with secret-bearing fields surfaced only as placeholder key lists,
   (c) PATCH does partial updates and preserves omitted secrets, (d) DELETE is
   idempotent enough (404-on-already-gone is acceptable). Record findings.
2. **Write the audit findings note** in this repo (`docs/terraform-provider-api-audit.md`)
   — a per-resource table mapping each Terraform resource onto its endpoints, secret
   handling, and import addressing, with explicit gap callouts.
3. **File follow-up API task specs** for any gap that would block a clean declarative
   lifecycle (perpetual diffs, broken `terraform import org/uid`, etc.), named per the
   `YYYY-MM-DD-NN-title.md` convention in `specs/todos/`. Do not block the provider on
   large API changes — gaps become small follow-ups.
4. **Archive** this spec to `specs/done/2026/05/` once the audit note and follow-up
   task(s) are committed. Provider repo bootstrap + resources + acceptance tests + docs
   (plan steps 2-4 in the section above) happen in `terraform-provider-solidping`, out
   of this tree, and are not gated by this spec's archival.
