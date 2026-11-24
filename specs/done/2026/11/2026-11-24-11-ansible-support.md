---
model: sonnet
effort: high
---

# Ansible users have no first-class way to deploy or configure SolidPing

GitHub Issue: https://github.com/fclairamb/solidping/issues/1013
GitHub Issue: https://github.com/fclairamb/solidping/issues/1015

## Source

- #1013: a self-hoster who had asked for "a full API" answered the Terraform/OpenTofu provider
  announcement (#958) on feddit.org with "nice, but I'm more of an Ansible user". The interim
  answer given was the `ansible.builtin.uri` module against the REST API, with
  `PUT /api/v1/orgs/{org}/checks/{slug}` as an idempotent upsert. The issue asks for an
  `fclairamb.solidping` collection mirroring the provider, plus a docs page in the meantime.
- #1015 (Florent, 2026-11-24): the same request, in three layers ordered by value/cost:
  1. a **deployment** collection (`server` role: binary or container install,
     `solidping service install`, templated `config.env` with `SP_*` env vars, restart handler,
     check mode, molecule tests);
  2. **API modules** mirroring the Terraform provider (check, notification_channel, status_page,
     status_page_section, maintenance_window, plus info/lookup modules);
  3. **Event-Driven Ansible**: document webhook → EDA rulebook → remediation playbook.

The two issues ask for the same thing; #1015 is the superset. One spec covers both.

## Problem

There is nothing Ansible-specific in the repo today:

- The IaC surface exists: the REST API with the upsert route `orgChecks.PUT("/:slug", checksHandler.UpsertCheck)`
  (`server/internal/app/server.go:1351`), the OpenAPI spec `server/internal/app/openapi/openapi.yaml`,
  the Terraform provider (separate repo `fclairamb/terraform-provider-solidping`, documented in
  `web/docs/docs/terraform.md`), and the config-as-code CLI (`sp checks export|validate|diff`,
  `sp apply -f`, `web/docs/docs/features/config-as-code.md`).
- No docs page shows how to drive any of these from Ansible, though two routes already work
  idempotently: `ansible.builtin.uri` with `PUT …/checks/{slug}`, and `sp apply -f` from a
  templated YAML file (its plan output can feed `changed_when`).
- There is no Galaxy collection, no `server` role, and no EDA example. The alert webhook
  channel exists (`web/docs/docs/configuration/notifications.md`) but nothing documents
  feeding it to `ansible.eda.webhook`.

## Why a spec

- New distribution artifact and a new code base (an Ansible collection, probably its own repo
  like the Terraform provider), well beyond ~4 files / ~150 lines.
- Product decisions not settled by the reporters: namespace, repo location, generated vs
  hand-written modules, molecule driver.

## Proposal

Deliver in this order, each step shippable alone:

1. **Docs first (in this repo)**: `web/docs/docs/ansible.md` (next to `terraform.md`, linked from
   it and from `config-as-code.md`):
   - auth: an org-scoped PAT (Account → Tokens), kept in Ansible Vault;
   - option A: a `uri` task per check with `PUT /api/v1/orgs/{org}/checks/{slug}`, `status_code: [200, 201]`,
     `changed_when` on the response (201 = created; compare for updates);
   - option B: template a config-as-code YAML and run `sp apply -f` (`sp checks diff` in check mode);
   - option C (EDA): a webhook notification channel pointed at an `ansible.eda.webhook` source,
     with an example rulebook matching on the incident payload's check slug/state and running a
     remediation playbook. Document the payload fields the rulebook reads from the real webhook
     schema.
   Each snippet must be run once against a local server before it is committed.
2. **Collection repo `fclairamb/ansible-collection-solidping`** (namespace per the open question):
   - `roles/server`: arch-aware release binary download (amd64, arm64, riscv64) or container,
     `solidping service install`, `config.env` template from `SP_*` variables, enabled service,
     restart handler, check mode supported; molecule scenario.
   - modules `check`, `notification_channel`, `status_page`, `status_page_section`,
     `maintenance_window` (`state: present|absent`, diff support, check mode) and `*_info`
     modules, mapped from the Terraform provider's resource schemas and the OpenAPI spec; a
     shared `module_utils` client (base URL, token, org, TLS options).
   - CI: ansible-test sanity + units, molecule, integration tests against a SolidPing container.
   - Release to Galaxy on tag.
3. **Venues** (Florent, after release): Galaxy, awesome-ansible, r/ansible.

## Open questions

1. **Collection namespace**: `fclairamb.solidping` or `solidping.solidping`?
   Recommended: `fclairamb.solidping`, matching the Terraform provider (`fclairamb/solidping`)
   and the GitHub org. A `solidping` Galaxy namespace looks more official but needs a namespace
   request and splits the brand from the provider.
2. **Modules generated from OpenAPI or hand-written?** Recommended: hand-written, five resources,
   mirroring the provider's schemas. Generation gives worse idempotency/diff behavior and more
   noise; the surface is small.
3. **Molecule driver**: docker or podman? Recommended: docker, which GitHub runners provide
   out of the box; systemd-in-container for the service role uses an image with systemd as PID 1.
4. **Where does the collection live?** Recommended: its own repo, like the Terraform provider;
   only the docs page lives here.

## Resolved open questions

1. Collection namespace: use `fclairamb.solidping`, matching the Terraform provider. (autopilot: recommended answer)
2. Modules: hand-written, five resources, mirroring the provider's schemas; no OpenAPI generation. (autopilot: recommended answer)
3. Molecule driver: docker; the service role uses an image with systemd as PID 1. (autopilot: recommended answer)
4. Location: the collection lives in its own repo, like the Terraform provider; only the docs page lives here. (autopilot: recommended answer)

## Closing the issue

This spec closes #1013 and #1015. The implementing PR body **must** carry one `Closes #1013` and
one `Closes #1015` line so the squash-merge closes them. When the spec is archived to
`specs/done/`, verify each issue is closed and, if the merge did not close it, close it by hand:

    gh issue close <number> --comment "Implemented by <PR or commit>; spec: <archived spec path>"
