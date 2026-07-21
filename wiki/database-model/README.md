# Database Model

Index for the SolidPing database schema: **50 tables**, grouped by domain. Each
child page documents its tables with a purpose line, a column table, foreign
keys, and notable indexes or constraints.

## Pages

| Page | Tables | Contents |
|------|--------|----------|
| [core.md](core.md) | 4 | organizations, parameters, app_settings, files |
| [auth.md](auth.md) | 9 | users, user_providers, user_passkeys, user_tokens, user_contacts, organization_providers, organization_members, membership_requests, oauth_clients |
| [checks.md](checks.md) | 7 | checks, check_groups, check_jobs, check_dependencies, labels, check_labels, workers |
| [agents.md](agents.md) | 2 | agents, agent_enrollment_tokens |
| [results-incidents.md](results-incidents.md) | 6 | results, incidents, incident_notifications, incident_member_checks, severities, events |
| [notifications.md](notifications.md) | 10 | integrations, check_channels, escalation policies/steps/targets, on-call schedules/users/overrides, user_notification_routes, email_suppressions |
| [status-pages.md](status-pages.md) | 5 | status_pages, status_page_sections, status_page_resources, status_page_subscriber, status_updates |
| [maintenance.md](maintenance.md) | 2 | maintenance_windows, maintenance_window_checks |
| [discovery.md](discovery.md) | 1 | discovered_checks |
| [entitlements.md](entitlements.md) | 2 | org_entitlements, org_entitlement_audits |
| [jobs.md](jobs.md) | 2 | jobs, state_entries |
| [patterns.md](patterns.md) | — | Entity-relationship overview, design patterns, file locations |

## Entity Relationship Overview

```
organizations (root)
├── parameters (org-scoped config)
├── organization_providers (external identity providers)
├── org_entitlements (plan limits) → org_entitlement_audits
├── check_groups (flat check grouping)
├── checks
│   ├── labels (via check_labels M2M)
│   ├── check_dependencies (parent/child DAG)
│   ├── check_jobs (1:n per region)
│   ├── results (1:many check results)
│   ├── incidents (1:many downtime periods)
│   └── check_channels (M2M to integrations)
├── agents (deported, org-scoped) ← agent_enrollment_tokens
├── integrations
│   └── check_channels (M2M to checks)
├── escalation_policies
│   └── escalation_policy_steps → escalation_policy_targets
├── on_call_schedules
│   ├── on_call_schedule_users
│   └── on_call_schedule_overrides
├── severities (channel sets used by escalation steps)
├── status_pages
│   ├── status_page_sections → status_page_resources (links to checks)
│   ├── status_page_subscriber
│   └── status_updates
├── maintenance_windows
│   └── maintenance_window_checks (links to checks or check_groups)
├── discovered_checks (scan suggestions)
├── files, email_suppressions
├── jobs (background tasks)
├── events (audit log)
├── state_entries (KV store)
└── organization_members
    └── users
        ├── user_providers (OAuth links)
        ├── user_passkeys (WebAuthn)
        ├── user_tokens (API tokens)
        ├── user_contacts → user_notification_routes
        └── membership_requests

workers (global, not org-scoped) — execute check_jobs
app_settings, oauth_clients (global)
```

See [patterns.md](patterns.md) for the schema-wide conventions and where the
migrations and Go models live.
