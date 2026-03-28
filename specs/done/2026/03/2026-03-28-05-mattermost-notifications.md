# Mattermost Notification Integration

## Overview

[Mattermost](https://mattermost.com) is an open-source, self-hosted Slack alternative popular in enterprises, government agencies, and organizations with strict data sovereignty requirements. It's the #1 self-hosted team chat platform and supports incoming webhooks with a Slack-compatible format.

**Why Mattermost:**
- Supported by Uptime Kuma, Gatus, Healthchecks.io, and Statping (4+ competitors)
- Self-hosted first — aligns with SolidPing's open-source audience
- Popular in regulated industries (government, defense, healthcare, finance)
- Webhook format is Slack-compatible, making implementation straightforward
- Growing market: 800K+ servers deployed

---

## Connection Type

`mattermost`

### Settings (JSONB)

```json
{
  "webhook_url": "https://mattermost.example.com/hooks/xxx-generatedkey-xxx",
  "channel": "",
  "username": "SolidPing",
  "icon_url": ""
}
```

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `webhook_url` | yes | — | Mattermost incoming webhook URL |
| `channel` | no | — | Override channel (e.g., `#alerts`). Empty = webhook default channel |
| `username` | no | `SolidPing` | Display name for the bot posts |
| `icon_url` | no | — | Custom avatar URL for the bot |

---

## API Integration

Uses [Mattermost Incoming Webhooks](https://developers.mattermost.com/integrate/webhooks/incoming/).

### Endpoint

```
POST {webhook_url}
```

Content-Type: `application/json`

### Format

Mattermost webhooks accept Slack-compatible attachment format, making this very similar to the Discord implementation but with Slack-style attachments instead of embeds.

### Event Mapping

| SolidPing Event | Color | Emoji | Title |
|----------------|-------|-------|-------|
| `incident.created` | `#FF0000` (red) | `:red_circle:` | `[DOWN] {checkName}` |
| `incident.resolved` | `#00FF00` (green) | `:white_check_mark:` | `[RECOVERED] {checkName}` |
| `incident.escalated` | `#FFA500` (orange) | `:warning:` | `[ESCALATED] {checkName}` |
| `incident.reopened` | `#FFFF00` (yellow) | `:repeat:` | `[REOPENED] {checkName}` |

---

## Implementation

### File: `server/internal/notifications/mattermost.go`

```go
type MattermostSender struct {
    client *http.Client
}

type MattermostSettings struct {
    WebhookURL string `json:"webhook_url"`
    Channel    string `json:"channel"`
    Username   string `json:"username"`
    IconURL    string `json:"icon_url"`
}
```

### Message Payload

```json
{
  "channel": "#incidents",
  "username": "SolidPing",
  "icon_url": "https://solidping.com/icon.png",
  "attachments": [
    {
      "fallback": "[DOWN] api-health-check — Connection timeout after 10s",
      "color": "#FF0000",
      "title": ":red_circle: [DOWN] api-health-check",
      "title_link": "https://solidping.example.com/checks/{uid}",
      "fields": [
        {
          "short": true,
          "title": "Check",
          "value": "api-health-check"
        },
        {
          "short": true,
          "title": "Type",
          "value": "HTTP"
        },
        {
          "short": false,
          "title": "Cause",
          "value": "Connection timeout after 10s"
        },
        {
          "short": true,
          "title": "Failure Count",
          "value": "3"
        }
      ],
      "footer": "SolidPing",
      "ts": 1711631000
    }
  ]
}
```

### Resolution Payload

```json
{
  "attachments": [
    {
      "color": "#00FF00",
      "title": ":white_check_mark: [RECOVERED] api-health-check",
      "fields": [
        {
          "short": true,
          "title": "Duration",
          "value": "4m 23s"
        },
        {
          "short": true,
          "title": "Recovery",
          "value": "3 consecutive successes"
        }
      ]
    }
  ]
}
```

---

## Error Handling

| HTTP Status | Behavior |
|-------------|----------|
| 200 | Success (body is `ok`) |
| 400 | Permanent error — malformed payload or disabled webhook |
| 403 | Permanent error — webhook disabled or channel restriction |
| 404 | Permanent error — webhook deleted |
| 429 | Retryable — rate limited |
| 5xx | Retryable — server error |

Mattermost returns plain text error messages (not JSON). Parse the response body for error details.

---

## Dashboard UI

### Connection Setup Form

- **Webhook URL**: Text input, help text: "Mattermost → Integrations → Incoming Webhooks → Add"
- **Channel Override** (optional): Text input, help text: "Override the webhook's default channel (e.g., #alerts)"
- **Bot Username** (optional): Text input, default "SolidPing"
- **Bot Icon URL** (optional): Text input
- **Test** button: Sends a test attachment message

### Connection Icon

Use the Mattermost logo (blue circle with chat bubble).

---

## Implementation Notes

The Slack-compatible attachment format means much of the payload construction logic can be shared with or adapted from the existing Slack sender. Consider extracting a shared `buildAttachmentFields` helper if the overlap is significant, but only if both senders are being maintained simultaneously.

The key difference from Slack: Mattermost uses a simple webhook POST (no OAuth, no Block Kit, no thread management). This makes it a simpler implementation — closer to Discord than Slack in complexity.

---

## Testing

### Unit Tests

- Test attachment payload construction for each event type
- Test channel override inclusion/omission
- Test username and icon_url defaults
- Test color mapping per event type

### Integration Tests

- Mock HTTP server verifying JSON payload format
- Test error classification (text body parsing)
- Test with and without channel override

---

## Competitor Reference

Uptime Kuma, Gatus, Healthchecks.io, Statping — all support Mattermost. It fills the self-hosted team chat niche that Slack and Teams don't cover.
