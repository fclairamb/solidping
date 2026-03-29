# Google Chat Notification Integration

## Overview

Google Chat is the messaging platform for Google Workspace (formerly G Suite). Organizations using Google Workspace — estimated at 9M+ businesses — use Google Chat as their primary team communication tool. Adding Google Chat as a notification channel covers a large market segment that Slack and Teams don't reach.

**Why Google Chat:**
- Supported by Uptime Kuma, Gatus, and Better Stack
- Webhook-based integration (simple, no OAuth)
- Google Workspace is the #3 enterprise collaboration platform after Microsoft 365 and Slack
- Webhook API supports rich card formatting similar to Slack Block Kit

---

## Connection Type

`googlechat`

### Settings (JSONB)

```json
{
  "webhook_url": "https://chat.googleapis.com/v1/spaces/SPACE_ID/messages?key=KEY&token=TOKEN",
  "thread_key_enabled": true
}
```

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `webhook_url` | yes | — | Google Chat space webhook URL |
| `thread_key_enabled` | no | `true` | Group incident updates into a thread |

---

## API Integration

Uses the [Google Chat Webhooks API](https://developers.google.com/workspace/chat/quickstart/webhooks).

### Endpoint

POST to the webhook URL directly. No authentication headers needed — the key and token are embedded in the URL.

### Thread Support

Append `&threadKey={incidentUid}&messageReplyOption=REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD` to the webhook URL to group updates per incident.

### Event Mapping

| SolidPing Event | Card Color | Title |
|----------------|------------|-------|
| `incident.created` | Red | `[DOWN] {checkName}` |
| `incident.resolved` | Green | `[RECOVERED] {checkName}` |
| `incident.escalated` | Orange | `[ESCALATED] {checkName}` |
| `incident.reopened` | Yellow | `[REOPENED] {checkName}` |

---

## Implementation

### File: `server/internal/notifications/googlechat.go`

```go
type GoogleChatSender struct {
    client *http.Client
}

type GoogleChatSettings struct {
    WebhookURL       string `json:"webhook_url"`
    ThreadKeyEnabled bool   `json:"thread_key_enabled"`
}
```

### Message Payload (Card V2 Format)

```json
{
  "cardsV2": [
    {
      "cardId": "incident-{uid}",
      "card": {
        "header": {
          "title": "[DOWN] api-health-check",
          "subtitle": "HTTP check failed",
          "imageUrl": "https://solidping.com/icon-down.png",
          "imageType": "CIRCLE"
        },
        "sections": [
          {
            "widgets": [
              {
                "decoratedText": {
                  "topLabel": "Check",
                  "text": "api-health-check (HTTP)"
                }
              },
              {
                "decoratedText": {
                  "topLabel": "Cause",
                  "text": "Connection timeout after 10s"
                }
              },
              {
                "decoratedText": {
                  "topLabel": "Failure Count",
                  "text": "3"
                }
              },
              {
                "buttonList": {
                  "buttons": [
                    {
                      "text": "View in SolidPing",
                      "onClick": {
                        "openLink": {
                          "url": "https://solidping.example.com/checks/{uid}"
                        }
                      }
                    }
                  ]
                }
              }
            ]
          }
        ]
      }
    }
  ]
}
```

### Thread Key Handling

When `thread_key_enabled` is true, append query params to the webhook URL:

```go
func (s *GoogleChatSender) buildURL(settings GoogleChatSettings, incidentUID string) string {
    u := settings.WebhookURL
    if settings.ThreadKeyEnabled {
        u += "&threadKey=" + incidentUID +
            "&messageReplyOption=REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD"
    }
    return u
}
```

This groups all events for a single incident into a thread, keeping the space tidy.

---

## Error Handling

| HTTP Status | Behavior |
|-------------|----------|
| 200 | Success |
| 400 | Permanent error — malformed payload |
| 401/403 | Permanent error — webhook URL invalid or revoked |
| 404 | Permanent error — space deleted or webhook removed |
| 429 | Retryable — rate limited |
| 5xx | Retryable — Google server error |

Google Chat webhooks have a rate limit of ~1 message per second per webhook. The job queue's retry with backoff handles this naturally.

---

## Dashboard UI

### Connection Setup Form

- **Webhook URL**: Text input with help text: "Google Chat → Space → Apps & Integrations → Webhooks → Add webhook"
- **Thread Grouping**: Toggle (default on) — "Group incident updates into a thread"
- **Test** button: Sends a test card message

### Connection Icon

Use the Google Chat logo (green speech bubble with lines).

---

## Testing

### Unit Tests

- Test card payload construction for each event type
- Test thread key URL building
- Test URL validation (must start with `https://chat.googleapis.com/`)

### Integration Tests

- Mock HTTP server verifying card V2 format
- Test thread key appending logic
- Test error classification

---

## Competitor Reference

Uptime Kuma, Gatus, Better Stack — all support Google Chat.
