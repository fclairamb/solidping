---
sidebar_position: 7
title: Ansible
---

# Ansible

SolidPing has no Ansible collection yet, but everything it manages is reachable from Ansible
today, idempotently, through the REST API or the [config-as-code CLI](/features/config-as-code).
This page shows three ways, from the simplest to the most powerful:

| | Use it for | Needs on the target |
|---|---|---|
| [A. `ansible.builtin.uri`](#a-one-task-per-check-with-uri) | A few checks, no extra tool | nothing |
| [B. `sp apply`](#b-a-templated-manifest-with-sp-apply) | A whole organization, with drift detection | the `sp` binary |
| [C. Event-Driven Ansible](#c-event-driven-ansible-remediation-from-an-alert) | Running a playbook when a check fails | `ansible-rulebook` |

Already on Terraform or OpenTofu? See the [provider](/terraform) instead.

## Authentication

Create an organization-scoped personal access token (**Account → Tokens**, or
`POST /api/v1/orgs/{org}/tokens`) and keep it in Ansible Vault:

```yaml
# group_vars/all/vault.yml (ansible-vault encrypt)
solidping_token: "sp_..."
```

```yaml
# group_vars/all/solidping.yml
solidping_url: https://solidping.acme.com
solidping_org: acme
```

## A. One task per check with `uri`

`PUT /api/v1/orgs/{org}/checks/{slug}` creates the check when the slug is new and updates it
otherwise. It answers `201` when it created the check and `200` when it updated (or left
unchanged) an existing one.

```yaml
- name: Make sure the website check exists
  ansible.builtin.uri:
    url: "{{ solidping_url }}/api/v1/orgs/{{ solidping_org }}/checks/website"
    method: PUT
    headers:
      Authorization: "Bearer {{ solidping_token }}"
    body_format: json
    body:
      name: Website
      type: http
      config:
        url: https://www.acme.com
      period: "00:01:00"
    status_code: [200, 201]
  register: website_check
  changed_when: website_check.status == 201
  no_log: true
```

`changed_when` reports a creation only: the API does not say whether a `200` modified anything.
Use option B when you need `changed` to mean "drift was corrected". `no_log: true` keeps the
token out of the logs; remove it while debugging.

To delete a check, `DELETE /api/v1/orgs/{org}/checks/{checkUid}` with `status_code: [200, 204, 404]`.

## B. A templated manifest with `sp apply`

Template the [export-format manifest](/features/config-as-code), then let `sp apply` reconcile.
`sp apply --dry-run -o json` returns the plan with `created`, `updated` and `deleted` counters,
which is exactly what `changed_when` needs, and `--dry-run` makes it safe in check mode.

```yaml
- name: Render the monitoring manifest
  ansible.builtin.template:
    src: solidping.yaml.j2
    dest: /etc/solidping/config.yaml
    mode: "0600"

- name: Log the CLI in with the token
  ansible.builtin.command: sp auth login --token "{{ solidping_token }}"
  environment: &sp_env
    SOLIDPING_URL: "{{ solidping_url }}"
    SOLIDPING_ORG: "{{ solidping_org }}"
  changed_when: false
  no_log: true

- name: Plan the changes
  ansible.builtin.command: sp apply -f /etc/solidping/config.yaml --dry-run -o json
  environment: *sp_env
  register: sp_plan
  changed_when: false
  check_mode: false

- name: Apply them
  ansible.builtin.command: sp apply -f /etc/solidping/config.yaml --yes -o json
  environment: *sp_env
  when: >-
    (sp_plan.stdout | from_json).created > 0 or
    (sp_plan.stdout | from_json).updated > 0 or
    (sp_plan.stdout | from_json).deleted > 0
  changed_when: true
```

```yaml
# templates/solidping.yaml.j2
version: 2
checks:
{% for check in solidping_checks %}
  - slug: {{ check.slug }}
    name: {{ check.name }}
    type: http
    config:
      url: {{ check.url }}
    period: 1m
{% endfor %}
```

Two things worth knowing:

- `sp checks diff <file>` exits `0` for no drift, `1` for drift and `2` or more on error, so
  `failed_when: rc > 1` with `changed_when: rc == 1` turns it into a drift report.
- `sp apply --prune` deletes what the manifest no longer lists. Leave it out until the manifest is
  complete.

## C. Event-Driven Ansible: remediation from an alert

Point a [webhook integration](/configuration/notifications#webhooks) at an
[`ansible.eda.webhook`](https://ansible.readthedocs.io/projects/rulebook/) source, and run a
playbook when an incident opens.

```yaml
# rulebook.yml
- name: SolidPing remediation
  hosts: all
  sources:
    - ansible.eda.webhook:
        host: 0.0.0.0
        port: 5000
  rules:
    - name: Restart the service when the website check fails
      condition: >-
        event.payload.type == "incident.created"
        and event.payload.data.check.name == "Website"
      action:
        run_playbook:
          name: restart-website.yml
          extra_vars:
            incident_uid: "{{ event.payload.data.incident.uid }}"
            check_name: "{{ event.payload.data.check.name }}"
```

The rulebook reads these fields of the [webhook payload](/configuration/notifications#payload-format):

| Field | Meaning |
|---|---|
| `type` | Event: `incident.created`, `incident.resolved`, `incident.reopened`, `incident.escalated`... |
| `data.check.uid`, `data.check.name`, `data.check.type` | The check. The payload carries the name and UID, not the slug: match on one of them |
| `data.incident.uid`, `data.incident.kind` | The incident; `kind` is `check`, `degraded` or `slo_burn` |
| `data.incident.regions.failingRegions` | Which regions saw the failure |

Notes:

- The webhook source is plain HTTP and does not verify the
  [Standard Webhooks signature](/configuration/notifications#signature-verification). Run it on a
  private network or behind a reverse proxy that does the verification.
- A delivery counts as failed on a status of 300 or more, or after 30 seconds without an answer,
  so the playbook runs asynchronously: the source answers right away.
- Match on `incident.resolved` to run a follow-up (close a ticket, undo a failover).

## A collection

An `fclairamb.solidping` collection is planned, in its own repository like the
[Terraform provider](/terraform): a `server` role (install the binary or the container,
`solidping service install`, a `config.env` templated from `SP_*` variables) and modules mirroring the
provider (`check`, `notification_channel`, `status_page`, `status_page_section`,
`maintenance_window`, with `*_info` lookups). Until it ships, the three options above cover the same ground.
