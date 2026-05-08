# BetterStack — Integrations, Webhooks, Terraform, AI

The "everything around the alert" surfaces. Internal channels (call/SMS/email/push) and chat/paging integrations live in [alerting.md](alerting.md); this page is about outgoing webhooks, IaC, the sister Telemetry product, and the AI features layered on top.

## Outgoing webhooks

A first-class resource (`betteruptime_outgoing_webhook` in Terraform). Three immutable trigger types set at create time:

- `incident_change`
- `monitor_change`
- `on_call_change`

### Sub-event toggles

For `incident_change`:
- `on_incident_started`
- `on_incident_acknowledged`
- `on_incident_resolved`
- `on_incident_reopened`
- `on_incident_comment`

For `monitor_change`:
- `created`, `updated`, `paused`, `unpaused`, `deleted`
- `maintenance_started`, `maintenance_completed`

For `on_call_change`:
- Fires when on-call contacts change.

### Custom payload templating — `custom_webhook_template_attributes`

Full templating control:
- `body_template` — string with token substitution; full control of the JSON body.
- `headers_template` — `[{ name, value }]`
- `http_method` — `get`, `post`, `put`, `patch`, `head`
- `auth_username` / `auth_password` — basic auth only

### What's missing — and a SolidPing opportunity

**No HMAC payload signing.** Authentication is limited to basic auth or a static custom header. This is a gap relative to Stripe-style HMAC signing and a place SolidPing can do better cheaply: a per-webhook secret + a `X-SolidPing-Signature: t=…,v1=…` header.

**Retry / DLQ behavior is not documented.** No field for retry count, max attempts, or dead-letter destination.

## Terraform provider

Official: `BetterStackHQ/terraform-provider-better-uptime`. Active, MIT-licensed, monthly releases. Requires Terraform 0.14+. Provider config: `api_token` or `BETTERUPTIME_API_TOKEN` env.

### Resources (31)

Core:
- `betteruptime_monitor`, `betteruptime_monitor_group`
- `betteruptime_heartbeat`, `betteruptime_heartbeat_group`
- `betteruptime_status_page`, `betteruptime_status_page_group`, `betteruptime_status_page_section`, `betteruptime_status_page_resource`
- `betteruptime_policy`, `betteruptime_policy_group`
- `betteruptime_severity`, `betteruptime_severity_group`
- `betteruptime_on_call_calendar`
- `betteruptime_incoming_webhook`, `betteruptime_outgoing_webhook`
- `betteruptime_metadata`
- `betteruptime_catalog_attribute`, `betteruptime_catalog_record`, `betteruptime_catalog_relation`
- `betteruptime_team_member`

Integrations (13): `aws_cloudwatch`, `azure`, `datadog`, `elastic`, `email`, `google_monitoring`, `grafana`, `jira`, `new_relic`, `pagerduty`, `prometheus`, `slack`, `splunk_oncall`.

### Data sources (8)

`incoming_webhook`, `ip_list`, `monitor`, `on_call_calendar`, `policy`, `severity`, `slack_integration`, `team_member`.

**Asymmetric**: no data source for `outgoing_webhook`, `status_page`, escalation policy steps, or heartbeat. Drift detection is incomplete on those resources.

### Drift handling

Standard tfplugindocs schema. Read-only fields exempt from drift: `status`, `last_checked_at`, `paused_at`, `aggregate_state`, `id`, `incident_token`, `created_at`, `updated_at`. Anything else changes → in-place update.

Provider quirk: `policy_id` on a monitor is a `String` (not `Number`). Worth noting if SolidPing builds a compatibility shim.

### Importing existing accounts

**No import script generator.** To bring an existing account under TF, you `terraform import` resource by resource, by ID, manually. Significant friction for migration.

## Telemetry / Logtail

BetterStack rebranded Logtail to "Telemetry". It's a separate product line (logs, metrics, traces, OpenTelemetry-native) sold under one umbrella. Cross-product wiring is mostly UI-level rather than schema-deep.

### Cross-links on the incident detail
- "Create Linear ticket" with AI-suggested fix
- **Automated AI post-mortem** generated from incident timeline + linked Slack channel transcript
- Logs from a Telemetry source can be **embedded in the incident timeline** when correlated by tag (label/service name); not formally schema'd in the public API

### Post-mortem feature in Uptime alone

A markdown comment on the incident triggered by typing "post mortem" in a comment. No structured schema. Recommended sections (per docs): Timeline, Why this happened, Estimated costs, Prevention.

Incident.io-style "Slack war room auto-create" is *not* documented but ships in production (per marketing).

## AI features

All marketing claims, no API:

- **Smart incident merging** — 1-tap ack of N concurrent incidents (UI affordance on top of the `incident_group_id` data plane).
- **AI-powered incident silencing** — auto-snoozes alerts the user has trained as low-priority. Rate-feedback model.
- **AI post-mortems** — auto-drafts from timeline + Slack.
- **AI-suggested Linear ticket** on the incident detail page.
- **Anomaly detection** — log-based alerts in the Telemetry side; not in Uptime.

These are all UI/automation features layered on the data plane. The sound design lesson: **build the deterministic data model first, then add AI on top as an affordance**, not as the primary mechanism. Don't model AI silencing as a schema concept.

## Sources

- https://betterstack.com/docs/uptime/webhooks/
- https://github.com/BetterStackHQ/terraform-provider-better-uptime
- https://github.com/BetterStackHQ/terraform-provider-better-uptime/blob/master/docs/resources/betteruptime_outgoing_webhook.md
- https://registry.terraform.io/providers/BetterStackHQ/better-uptime/latest/docs
- https://betterstack.com/uptime
- https://betterstack.com/incident-silencing
- https://betterstack.com/telemetry
- https://betterstack.com/docs/uptime/post-mortems/
