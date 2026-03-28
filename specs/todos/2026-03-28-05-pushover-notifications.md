# Pushover Notification Integration

## Overview

[Pushover](https://pushover.net) is a push notification service for Android, iOS, and desktop. It's a one-time $5 purchase per platform (no subscription) and delivers real-time notifications with priority levels, sounds, and acknowledgment tracking. It's one of the most widely supported notification channels across monitoring tools.

**Why Pushover:**
- Supported by Uptime Kuma, Gatus, Healthchecks.io, Statping, and Better Stack (5+ competitors)
- One-time cost model makes it popular with individual developers and small teams
- Priority system with "Emergency" level that re-alerts until acknowledged — ideal for critical incidents
- Simple REST API (one endpoint, no OAuth)
- Supports delivery to individuals, groups, or devices

---

## Connection Type

`pushover`

### Settings (JSONB)

```json
{
  "user_key": "uQiRzpo4DXghDmr9QzzfQu27cmVRsG",
  "api_token": "azGDORePK8gMaC0QOYAMyEEuzJnyUi",
  "device": "",
  "sound_down": "siren",
  "sound_up": "magic",
  "priority_mapping": {
    "created": 1,
    "escalated": 2,
    "resolved": -1,
    "reopened": 1
  }
}
```

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `user_key` | yes | — | User or group key from Pushover dashboard |
| `api_token` | yes | — | Application API token (create a "SolidPing" app in Pushover) |
| `device` | no | — | Specific device name (empty = all devices) |
| `sound_down` | no | `siren` | Sound for down/escalated events |
| `sound_up` | no | `magic` | Sound for resolved events |
| `priority_mapping` | no | see above | Priority per event type |

### Pushover Priority Levels

| Value | Name | Behavior |
|-------|------|----------|
| `-2` | Lowest | No notification, no sound |
| `-1` | Low | No sound/vibration |
| `0` | Normal | Standard notification |
| `1` | High | Bypasses quiet hours |
| `2` | Emergency | Repeats until acknowledged (requires `retry` + `expire`) |

---

## API Integration

Uses the [Pushover Messages API](https://pushover.net/api).

### Endpoint

```
POST https://api.pushover.net/1/messages.json
```

### Event Mapping

| SolidPing Event | Priority | Title | Sound |
|----------------|----------|-------|-------|
| `incident.created` | `1` (High) | `[DOWN] {checkName}` | `siren` |
| `incident.escalated` | `2` (Emergency) | `[ESCALATED] {checkName}` | `siren` |
| `incident.resolved` | `-1` (Low) | `[RECOVERED] {checkName}` | `magic` |
| `incident.reopened` | `1` (High) | `[REOPENED] {checkName}` | `siren` |

---

## Implementation

### File: `server/internal/notifications/pushover.go`

```go
type PushoverSender struct {
    client *http.Client
}

type PushoverSettings struct {
    UserKey         string         `json:"user_key"`
    APIToken        string         `json:"api_token"`
    Device          string         `json:"device"`
    SoundDown       string         `json:"sound_down"`
    SoundUp         string         `json:"sound_up"`
    PriorityMapping map[string]int `json:"priority_mapping"`
}
```

### Request Payload (form-encoded)

```go
func (s *PushoverSender) Send(ctx context.Context, settings PushoverSettings, payload NotificationPayload) error {
    data := url.Values{
        "token":    {settings.APIToken},
        "user":     {settings.UserKey},
        "title":    {title},
        "message":  {body},
        "priority": {strconv.Itoa(priority)},
        "sound":    {sound},
        "url":      {dashboardURL},
        "url_title": {"View in SolidPing"},
        "html":     {"1"},
    }

    if settings.Device != "" {
        data.Set("device", settings.Device)
    }

    // Emergency priority requires retry and expire params
    if priority == 2 {
        data.Set("retry", "300")   // Retry every 5 minutes
        data.Set("expire", "3600") // Stop after 1 hour
    }

    resp, err := s.client.PostForm("https://api.pushover.net/1/messages.json", data)
    // ...
}
```

### Message Body (HTML)

```html
<b>Check:</b> api-health-check (HTTP)
<b>Cause:</b> Connection timeout after 10s
<b>Failures:</b> 3
<b>Started:</b> 2026-03-28 14:23:00 UTC
```

For resolved:

```html
<b>Check:</b> api-health-check (HTTP)
<b>Duration:</b> 4m 23s
<b>Successes:</b> 3 consecutive
```

---

## Error Handling

| HTTP Status | `status` field | Behavior |
|-------------|----------------|----------|
| 200, status=1 | Success | Message delivered |
| 200, status=0 | Check `errors` array | Permanent error (bad token, invalid user, etc.) |
| 429 | — | Retryable — rate limited (7,500 msgs/month per app) |
| 5xx | — | Retryable — server error |

Pushover returns errors in the response body even with HTTP 200:
```json
{ "status": 0, "errors": ["user identifier is not valid"] }
```

Always check the `status` field, not just the HTTP status code.

---

## Dashboard UI

### Connection Setup Form

- **API Token**: Password input, help text: "Create a SolidPing application at pushover.net/apps/build"
- **User/Group Key**: Password input, help text: "Found on your Pushover dashboard"
- **Device** (optional): Text input, help text: "Send to a specific device only"
- **Down Sound**: Dropdown of Pushover sounds (siren, alien, mechanical, etc.)
- **Up Sound**: Dropdown of Pushover sounds (magic, cashregister, etc.)
- **Test** button: Sends a test notification

### Connection Icon

Use the Pushover logo (speech bubble with "P").

---

## Testing

### Unit Tests

- Test form-encoded payload construction for each event type
- Test emergency priority includes `retry` and `expire`
- Test HTML body formatting
- Test sound selection based on event type
- Test response parsing (status=0 with errors)

### Integration Tests

- Mock HTTP server verifying form data
- Test error classification (status=0 vs HTTP errors)
- Test device filtering

---

## Competitor Reference

Uptime Kuma, Gatus, Healthchecks.io, Statping, Better Stack — all support Pushover. It's among the top 5 most commonly supported channels across monitoring tools.
