# Opsgenie Notification Integration

## Overview

Opsgenie (Atlassian) is one of the two dominant incident management platforms alongside PagerDuty. It's supported by Uptime Kuma, Gatus, Healthchecks.io, and Better Stack. Adding it covers teams in the Atlassian ecosystem (Jira, Confluence, Statuspage) who use Opsgenie for on-call management.

**Why Opsgenie specifically:**
- Atlassian ecosystem integration — teams already using Jira/Confluence naturally use Opsgenie
- Supported by 5+ competing monitoring tools
- Different API model from PagerDuty (Alert API vs Events API) — not a copy-paste
- Free tier available (up to 5 users) lowers adoption friction

---

## Connection Type

`opsgenie`

### Settings (JSONB)

```json
{
  "api_key": "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
  "region": "us",
  "priority": "P2",
  "responders": [
    { "type": "team", "id": "team-uuid" }
  ],
  "tags": ["solidping", "monitoring"]
}
```

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `api_key` | yes | — | Opsgenie API integration key (not the account API key) |
| `region` | no | `us` | API region: `us` (`api.opsgenie.com`) or `eu` (`api.eu.opsgenie.com`) |
| `priority` | no | `P3` | Default alert priority: `P1`-`P5` |
| `responders` | no | `[]` | Teams/users/schedules to notify |
| `tags` | no | `["solidping"]` | Tags attached to all alerts |

---

## API Integration

Uses the [Opsgenie Alert API v2](https://docs.opsgenie.com/docs/alert-api).

### Base URLs

- US: `https://api.opsgenie.com/v2/alerts`
- EU: `https://api.eu.opsgenie.com/v2/alerts`

### Authentication

```
Authorization: GenieKey {api_key}
```

### Event Mapping

| SolidPing Event | Opsgenie Action | Endpoint |
|----------------|-----------------|----------|
| `incident.created` | Create Alert | `POST /v2/alerts` |
| `incident.escalated` | Add Note + Update Priority | `POST /v2/alerts/{alias}/notes` |
| `incident.resolved` | Close Alert | `POST /v2/alerts/{alias}/close` |
| `incident.reopened` | Create Alert (new) | `POST /v2/alerts` |

### Deduplication

Use `alias` field set to the SolidPing incident UID. Opsgenie deduplicates alerts with the same alias — if an alert with that alias already exists and is open, Opsgenie increments its count instead of creating a duplicate.

---

## Implementation

### File: `server/internal/notifications/opsgenie.go`

```go
type OpsgenieSender struct {
    client *http.Client
}

type OpsgenieSettings struct {
    APIKey     string              `json:"api_key"`
    Region     string              `json:"region"`
    Priority   string              `json:"priority"`
    Responders []OpsgenieResponder `json:"responders"`
    Tags       []string            `json:"tags"`
}

type OpsgenieResponder struct {
    Type string `json:"type"` // team, user, escalation, schedule
    ID   string `json:"id"`
}
```

### Create Alert Payload

```json
{
  "message": "[DOWN] api-health-check",
  "alias": "incident-uid-here",
  "description": "Check 'api-health-check' (HTTP) is down.\nCause: Connection timeout after 10s\nFailure count: 3",
  "priority": "P2",
  "responders": [{ "type": "team", "id": "..." }],
  "tags": ["solidping", "http"],
  "details": {
    "checkUid": "...",
    "checkName": "api-health-check",
    "checkType": "http",
    "incidentUid": "...",
    "failureCount": "3",
    "dashboardUrl": "https://..."
  },
  "source": "SolidPing"
}
```

### Close Alert Payload

```json
{
  "note": "Recovered after 4m 23s (3 consecutive successes)"
}
```

### Escalation (Add Note)

```json
{
  "body": "Escalated: 5 consecutive failures in 2m 30s",
  "user": "SolidPing"
}
```

On escalation, also update priority to one level higher (e.g., P3 → P2) via `PATCH /v2/alerts/{alias}`.

---

## Error Handling

| HTTP Status | Behavior |
|-------------|----------|
| 200-202 | Success |
| 401 | Permanent error — bad API key |
| 403 | Permanent error — insufficient permissions |
| 404 (on close) | Ignore — alert may have been manually closed |
| 409 | Ignore — alert already exists (dedup working) |
| 422 | Permanent error — bad payload |
| 429 | Retryable — rate limited (respect `Retry-After` header) |
| 5xx | Retryable — server error |

---

## Dashboard UI

### Connection Setup Form

- **API Key**: Password input with "Get from Opsgenie → Settings → Integrations → API" help text
- **Region**: Dropdown: US / EU
- **Priority**: Dropdown: P1 (Critical) / P2 (High) / P3 (Medium) / P4 (Low) / P5 (Informational)
- **Tags**: Tag input (comma-separated)
- **Test** button: Creates and immediately closes a test alert

### Connection Icon

Use the Opsgenie logo (blue/teal shield icon).

---

## Testing

### Unit Tests

- Test payload construction for each event type
- Test region URL selection
- Test priority escalation logic
- Test alias-based deduplication key generation

### Integration Tests

- Mock HTTP server verifying correct endpoints, headers, and payloads
- Test error classification (retryable vs permanent)
- Test 429 rate limiting behavior

---

## Competitor Reference

Uptime Kuma, Gatus, Healthchecks.io, Better Stack, StatusCake, Checkly — all support Opsgenie.
