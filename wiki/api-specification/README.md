# API Specification

Index for the SolidPing REST API. All API routes are prefixed with `/api/v1`
unless otherwise noted. Organization-scoped routes use `:org` to refer to the
organization slug. Each child page documents one domain: its routes, auth
level, query parameters, and response shapes.

## Conventions

- **Pagination**: Cursor-based. Use `cursor` and `limit` query parameters. Responses include `hasMore` and `cursor` for the next page. Endpoints that previously used `?size=` still accept it as a deprecated alias.
- **Filtering**: Multi-value filters use comma-separated values in singular form (e.g., `?checkUid=a,b`).
- **Search**: Use `q` for free-text search.
- **Optional includes**: Use `with` to request related data (e.g., `?with=last_result,check`).
- **JSON conventions**: camelCase for all JSON properties and query parameters.
- **List responses**: Always wrapped in `{ "data": [...] }`, never bare arrays.
- **Updates**: Use `PATCH` for partial updates.
- **Errors**: See [errors.md](errors.md).

**Auth legend** used throughout: `public` (no credentials), `required` (any
authenticated org member), `admin` (org admin role), `super-admin` (server
super-admin), `service-token` (shared-secret bearer), `signature` (HMAC or
provider signature in the request).

## Pages

| Page | Contents |
|------|----------|
| [management.md](management.md) | Health, version, limits, memory, bug report, feature flags, scheduling cost, email preview |
| [auth.md](auth.md) | Login, registration, tokens, 2FA, passkeys, per-user UI state, OAuth providers, OIDC, SAML |
| [orgs.md](orgs.md) | Organizations, settings, org tokens, invitations, members, membership requests |
| [entitlements.md](entitlements.md) | Per-org limits, the billing-service write API, and the audit log |
| [checks.md](checks.md) | Checks, validate, export/import/apply, dependencies, clone, labels, check types, groups, severities, badges, availability |
| [results-incidents.md](results-incidents.md) | Results, incidents and their actions, events, and the live-update WebSocket |
| [events-catalogue.md](events-catalogue.md) | The audit trail: every event type, payload keys, redaction rules, flood control, retention |
| [notifications.md](notifications.md) | Notification routes and contacts, notification history, web push, email suppressions, public unsubscribe |
| [on-call.md](on-call.md) | Escalation policies, on-call schedules, overrides, iCal feeds |
| [status-pages.md](status-pages.md) | Status pages, sections, resources, subscribers, status updates, public views and feeds |
| [maintenance.md](maintenance.md) | Maintenance windows and their check associations |
| [slos.md](slos.md) | Service-level objectives, error budgets, and scheduled uptime reports |
| [integrations.md](integrations.md) | Integrations/channels, Slack app endpoints, Freebox pairing |
| [agents.md](agents.md) | Deported agent WebSocket, private regions, enrollment tokens, agent inventory |
| [discovery.md](discovery.md) | Network discovery scans and discovered checks |
| [jobs.md](jobs.md) | Org jobs, admin job observability, system-wide job observability |
| [files.md](files.md) | Generic file storage and signed public reads |
| [heartbeat.md](heartbeat.md) | Heartbeat / cron ping endpoints |
| [mcp-oauth.md](mcp-oauth.md) | MCP endpoint and the embedded OAuth 2.1 authorization server |
| [system.md](system.md) | System parameters, email inbox, activation, scheduling lane load, regions |
| [test-and-static.md](test-and-static.md) | Test-mode endpoints, SPA/static routes, metrics, docs |
| [errors.md](errors.md) | Error envelope and standard error codes |
