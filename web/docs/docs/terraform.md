---
sidebar_position: 6
title: Terraform / OpenTofu
---

# Terraform and OpenTofu provider

The `fclairamb/solidping` provider manages SolidPing monitoring as code, from Terraform or
OpenTofu: checks, notification channels, status pages and their sections, and maintenance
windows. It talks to the same REST API as the dashboard and the [CLI](/cli).

- Source and issues: [github.com/fclairamb/terraform-provider-solidping](https://github.com/fclairamb/terraform-provider-solidping)
- Reference documentation: [Terraform Registry](https://registry.terraform.io/providers/fclairamb/solidping/latest/docs)

## Setting up

The provider works on one organization of one SolidPing server. It authenticates with an
organization-scoped personal access token, created in the dashboard (**Account → Tokens**) or with
`POST /api/v1/orgs/{org}/tokens`.

```hcl
terraform {
  required_providers {
    solidping = {
      source  = "fclairamb/solidping"
      version = "~> 0.1"
    }
  }
}

provider "solidping" {
  endpoint = "https://solidping.acme.com" # or SOLIDPING_ENDPOINT
  org      = "acme"                       # or SOLIDPING_ORG
  token    = var.solidping_token          # or SOLIDPING_TOKEN
}

variable "solidping_token" {
  type      = string
  sensitive = true
}
```

The same block works with `terraform init` and `tofu init`. OpenTofu resolves `fclairamb/solidping`
from its own registry; if `tofu init` reports it missing there, write the full address
`registry.terraform.io/fclairamb/solidping` as the `source`, which OpenTofu installs and verifies
just as well.

## What it manages

| Resource | Notes |
|---|---|
| `solidping_check` | Any [check type](/features/check-types). Type-specific settings in `config` (JSON), secrets in `config_sensitive`. |
| `solidping_channel` | Every [notification channel](/configuration/notifications) except Slack, which is connected by installing the Slack app. |
| `solidping_status_page` | A [status page](/features/status-pages). |
| `solidping_status_page_section` | A section and its components: a hand-picked list of checks, or a selector. |
| `solidping_maintenance_window` | A one-off or recurring [maintenance window](/features/maintenance-windows), with its checks and check groups. |

The data sources `solidping_check`, `solidping_channel` and `solidping_status_page` look up
existing objects by UID, slug or name.

Escalation policies and on-call schedules are not covered yet.

## Example

```hcl
resource "solidping_check" "website" {
  name   = "Website"
  type   = "http"
  config = jsonencode({ url = "https://www.acme.com/health" })
}

resource "solidping_channel" "ops_webhook" {
  type     = "webhook"
  name     = "Ops webhook"
  settings = jsonencode({ url = "https://hooks.acme.com/solidping" })
}

resource "solidping_status_page" "public" {
  name = "Acme status"
}

resource "solidping_status_page_section" "core" {
  status_page_uid = solidping_status_page.public.id
  name            = "Core services"
  resources       = [{ check_uid = solidping_check.website.id }]
}

resource "solidping_maintenance_window" "weekly_patching" {
  title          = "Weekly patching"
  start_at       = "2027-01-03T02:00:00Z"
  end_at         = "2027-01-03T04:00:00Z"
  recurrence     = "weekly"
  recurrence_end = "2027-12-31T00:00:00Z"
  check_uids     = [solidping_check.website.id]
}
```

## Secrets

SolidPing stores secrets encrypted and never returns them: reading a check or a channel gives
its public settings and only the *names* of its secret keys. The provider splits each JSON object
in two attributes:

- `config` / `settings` hold the public keys. Only the keys you wrote are compared with the
  server, so the defaults and generated values the server adds never show up as drift.
- `config_sensitive` / `settings_sensitive` hold the secret keys. They are marked sensitive, kept
  in the state as written and never refreshed. Removing a key clears the secret on the server.

## Importing existing objects

Every resource is imported by organization and UID:

```bash
terraform import solidping_check.website acme/<checkUid>
terraform import solidping_status_page_section.core acme/<statusPageUid>/<sectionUid>
```

The UID of an object is in its dashboard URL, and in the output of the [CLI](/cli) and the API.

## Terraform or `sp apply`?

SolidPing also has its own [config as code](/features/config-as-code): `sp apply -f` on a YAML
manifest in the export format, for checks, channels, maintenance windows and status pages. Use it
when SolidPing is all the manifest describes and you want the file to round-trip with an export.
Use the provider when SolidPing is part of a wider Terraform or OpenTofu setup, for example to
create a check next to the service it watches, from that service's outputs.
