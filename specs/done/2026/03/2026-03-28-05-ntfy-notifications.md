# Ntfy Notification Integration

## Overview

[Ntfy](https://ntfy.sh) (pronounced "notify") is an open-source HTTP-based pub-sub notification service. Users can self-host it or use the free public instance at `ntfy.sh`. It delivers push notifications to phones (Android/iOS apps) and desktops (PWA) with zero signup required — just pick a topic name.

**Why ntfy:**
- Supported by Uptime Kuma, Gatus, and Healthchecks.io
- Perfect fit for SolidPing's self-hosted/open-source audience
- Dead simple: one HTTP POST = one notification. No API keys, no OAuth, no SDKs
- Self-hostable (pairs with self-hosted SolidPing)
- Supports priority levels, tags/emojis, action buttons, and click URLs
- Free public instance (`ntfy.sh`) requires zero setup

---

## Connection Type

`ntfy`

### Settings (JSONB)

```json
{
  "server_url": "https://ntfy.sh",
  "topic": "solidping-alerts",
  "access_token": "",
  "priority_mapping": {
    "created": "urgent",
    "escalated": "high",
    "resolved": "default",
    "reopened": "high"
  }
}
```

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `server_url` | no | `https://ntfy.sh` | Ntfy server URL (public or self-hosted) |
| `topic` | yes | — | Topic name to publish to |
| `access_token` | no | — | Bearer token for authenticated topics |
| `priority_mapping` | no | see above | Priority per event type: `min`, `low`, `default`, `high`, `urgent` |

---

## API Integration

Uses the [ntfy publish API](https://docs.ntfy.sh/publish/).

### Endpoint

```
POST {server_url}/{topic}
```

### Authentication (optional)

```
Authorization: Bearer {access_token}
```

### Event Mapping

| SolidPing Event | Ntfy Priority | Tags (emojis) | Title |
|----------------|---------------|----------------|-------|
| `incident.created` | `urgent` (5) | `rotating_light` | `[DOWN] {checkName}` |
| `incident.resolved` | `default` (3) | `white_check_mark` | `[RECOVERED] {checkName}` |
| `incident.escalated` | `high` (4) | `warning` | `[ESCALATED] {checkName}` |
| `incident.reopened` | `high` (4) | `repeat` | `[REOPENED] {checkName}` |

---

## Implementation

### File: `server/internal/notifications/ntfy.go`

```go
type NtfySender struct {
    client *http.Client
}

type NtfySettings struct {
    ServerURL       string            `json:"server_url"`
    Topic           string            `json:"topic"`
    AccessToken     string            `json:"access_token"`
    PriorityMapping map[string]string `json:"priority_mapping"`
}
```

### Publish via HTTP Headers

Ntfy supports setting all parameters via HTTP headers, which is cleaner than JSON:

```go
func (s *NtfySender) Send(ctx context.Context, settings NtfySettings, payload NotificationPayload) error {
    url := settings.ServerURL + "/" + settings.Topic

    req, _ := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(body))
    req.Header.Set("Title", title)
    req.Header.Set("Priority", priority)
    req.Header.Set("Tags", tags)
    req.Header.Set("Click", dashboardURL)
    req.Header.Set("Actions", fmt.Sprintf("view, View in SolidPing, %s", dashboardURL))

    if settings.AccessToken != "" {
        req.Header.Set("Authorization", "Bearer "+settings.AccessToken)
    }

    resp, err := s.client.Do(req)
    // ...
}
```

### Message Body

Plain text body with incident details:

```
Check: api-health-check (HTTP)
Cause: Connection timeout after 10s
Failure count: 3
Started: 2026-03-28 14:23:00 UTC
```

For resolved events:

```
Check: api-health-check (HTTP)
Duration: 4m 23s
Recovery threshold: 3 consecutive successes
```

---

## Error Handling

| HTTP Status | Behavior |
|-------------|----------|
| 200 | Success |
| 401/403 | Permanent error — bad access token or topic requires auth |
| 404 | Permanent error — topic not found (self-hosted only) |
| 413 | Permanent error — message too large (4096 bytes limit) |
| 429 | Retryable — rate limited (public instance: ~250 msgs/day) |
| 5xx | Retryable — server error |

---

## Dashboard UI

### Connection Setup Form

- **Server URL**: Text input, default `https://ntfy.sh`, help text: "Use ntfy.sh (free) or your self-hosted instance"
- **Topic**: Text input, help text: "Choose a unique topic name. Anyone with the name can subscribe."
- **Access Token**: Password input (optional), help text: "Required only for password-protected topics"
- **Subscribe link**: After saving, show "Subscribe: open `{server_url}/{topic}` in the ntfy app"
- **Test** button: Sends a test notification

### Connection Icon

Use the ntfy logo (bell icon).

---

## Testing

### Unit Tests

- Test header construction for each event type
- Test priority mapping (custom and default)
- Test authentication header inclusion/omission
- Test URL construction with custom server

### Integration Tests

- Mock HTTP server verifying headers and body
- Test error classification
- Test with and without access token

---

## Competitor Reference

Uptime Kuma, Gatus, Healthchecks.io — all support ntfy. It's becoming a standard in the self-hosted ecosystem.
