# Telegram Notifications

## Overview

Add Telegram as a notification channel for incident alerts. Uses the Telegram Bot API with a simple bot token + chat ID configuration. No OAuth flow needed — users create a bot via @BotFather and paste the token.

## Goals

1. Send notifications to Telegram chats/groups/channels when incidents occur
2. Support all incident event types: `incident.created`, `incident.resolved`, `incident.escalated`, `incident.reopened`
3. Rich HTML-formatted messages with status emojis and incident links
4. Simple setup: bot token + chat ID, no complex OAuth

---

## Connection Settings

Connection type: `telegram`

### Settings (JSONB)

```json
{
  "bot_token": "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11",
  "chat_id": "-1001234567890",
  "parse_mode": "HTML",
  "disable_notification": false,
  "thread_id": 0
}
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `bot_token` | string | yes | — | Telegram Bot API token from [@BotFather](https://t.me/BotFather) |
| `chat_id` | string | yes | — | Target chat, group, or channel ID |
| `parse_mode` | string | no | `"HTML"` | Message format: `HTML` or `MarkdownV2` |
| `disable_notification` | bool | no | `false` | Send message silently (no notification sound) |
| `thread_id` | int | no | `0` | Message thread ID for topic-based groups (0 = no thread) |

### Setup Instructions (for users)

1. Open Telegram and message [@BotFather](https://t.me/BotFather)
2. Send `/newbot` and follow the prompts to create a bot
3. Copy the bot token provided by BotFather
4. Add the bot to your target chat/group/channel
5. Get the chat ID (can use the bot API `getUpdates` or tools like [@userinfobot](https://t.me/userinfobot))
6. Enter bot token and chat ID in SolidPing connection settings

---

## API

Uses the existing connection CRUD endpoints — no new API routes needed.

### Telegram Bot API Endpoints Used

| Method | Endpoint | Purpose |
|--------|----------|---------|
| POST | `/bot<token>/sendMessage` | Send notification message |
| POST | `/bot<token>/editMessageText` | Update existing message (on resolution) |
| POST | `/bot<token>/getMe` | Validate bot token (test connection) |

Base URL: `https://api.telegram.org`

---

## Message Format

### Incident Created
```
🔴 Incident Created

Check: Website Homepage
Status: DOWN
Details: HTTP 503 — Service Unavailable

🔗 View Incident
```

### Incident Escalated
```
🟠 Incident Escalated

Check: Website Homepage
Status: DOWN (5 consecutive failures)
Details: HTTP 503 — Service Unavailable
Duration: 5m 12s

🔗 View Incident
```

### Incident Resolved
```
🟢 Incident Resolved

Check: Website Homepage
Status: UP
Duration: 12m 34s

🔗 View Incident
```

### Incident Reopened
```
🔴 Incident Reopened

Check: Website Homepage
Status: DOWN (relapse)
Details: Connection timeout

🔗 View Incident
```

### HTML Template

```html
<b>🔴 Incident Created</b>

<b>Check:</b> Website Homepage
<b>Status:</b> DOWN
<b>Details:</b> HTTP 503 — Service Unavailable

<a href="https://app.solidping.com/orgs/default/incidents/abc-123">🔗 View Incident</a>
```

---

## Implementation

### Backend

#### 1. TelegramSender (`back/internal/notifications/telegram.go`)

Implements `Sender` interface from `sender.go`:

```go
type TelegramSender struct{}

func (s *TelegramSender) Send(ctx context.Context, jctx *jobdef.JobContext, payload *Payload) error
```

- Parse `bot_token` and `chat_id` from `payload.Connection.Settings`
- Build HTML message based on `payload.EventType`
- POST to `https://api.telegram.org/bot<token>/sendMessage`
- No external dependencies — plain `net/http` with JSON body
- **Threading per incident**: The first notification for an incident (`incident.created`) sends a new message and stores the returned `message_id` in the incident's notification state entries (same pattern as Slack's `message_ts`). All subsequent notifications for that incident (`escalated`, `reopened`, `resolved`) are sent as replies to the original message using the `reply_to_message_id` parameter. This keeps all updates for a given incident grouped in a single Telegram thread.
- On resolution: also attempt `editMessageText` on the original message to update its status (e.g., strike through or mark as resolved)
- Use `IsNetworkError()` for retry classification

#### 2. Register sender (`back/internal/notifications/registry.go`)

Add to registry map:
```go
"telegram": &TelegramSender{},
```

#### 3. Connection type (`back/internal/db/models/integration.go`)

Add `telegram` to valid connection types.

### Dashboard (`apps/dash0/`)

#### Connection Form

- Add Telegram to the connection type selector (with Telegram icon)
- Form fields:
  - Bot Token (password input, required)
  - Chat ID (text input, required)
  - Parse Mode (dropdown: HTML / MarkdownV2, default HTML)
  - Silent Notifications (toggle, default off)
  - Thread ID (number input, optional, for topic groups)
- **Test Connection** button: calls `getMe` to validate the bot token, then sends a test message

#### i18n

Add translation keys for:
- Connection type name
- Form field labels and placeholders
- Setup help text
- Test message content

---

## Threading

All notifications for a given incident are grouped in a single Telegram thread:

1. **`incident.created`** — Sends a new message. Stores the returned `message_id` and `chat_id` in the incident's notification state entries (same pattern as Slack stores `message_ts` and `channel_id`).
2. **`incident.escalated`** / **`incident.reopened`** — Sends a reply to the original message using `reply_to_message_id`, keeping updates visible as a thread under the initial alert.
3. **`incident.resolved`** — Sends a reply to the original message AND edits the original message text to reflect the resolved status (adds a ✅ prefix and resolution info).

This ensures that a single incident never produces scattered standalone messages — all context stays in one thread, making it easy to follow the incident lifecycle.

### State Entry Keys

| Key | Value | Description |
|-----|-------|-------------|
| `telegram_message_id` | int | Message ID of the initial `incident.created` message |
| `telegram_chat_id` | string | Chat ID where the message was sent |

---

## Rate Limiting

Telegram enforces rate limits:
- **30 messages/second** to different chats
- **1 message/second** per individual chat
- **20 messages/minute** to the same group

The existing job queue with retry logic handles this naturally. On 429 responses, respect the `retry_after` field from the Telegram API response.

---

## Error Handling

| Telegram Error | Action |
|----------------|--------|
| 401 Unauthorized | Invalid bot token — mark as permanent failure, log warning |
| 400 Bad Request (chat not found) | Invalid chat_id — mark as permanent failure |
| 403 Forbidden | Bot was removed from chat — mark as permanent failure |
| 429 Too Many Requests | Rate limited — retry after `retry_after` seconds |
| 5xx / network errors | Transient — retry via `IsNetworkError()` |

---

## Files to Create/Modify

| File | Action |
|------|--------|
| `back/internal/notifications/telegram.go` | **Create** — TelegramSender implementation |
| `back/internal/notifications/telegram_test.go` | **Create** — Unit tests |
| `back/internal/notifications/registry.go` | **Modify** — Register telegram sender |
| `back/internal/db/models/integration.go` | **Modify** — Add telegram type |
| `apps/dash0/src/pages/connections/` | **Modify** — Add Telegram connection form |
| `apps/dash0/src/locales/en/` | **Modify** — Add i18n keys |
| `apps/dash0/src/locales/fr/` | **Modify** — Add i18n keys |

---

## Testing

### Unit Tests
- Message formatting for each event type
- Settings parsing and validation
- Error classification (permanent vs retryable)

### Integration Tests
- Mock HTTP server simulating Telegram Bot API
- Verify correct API calls (sendMessage, editMessageText)
- Verify retry behavior on 429 and network errors

### Manual Testing
1. Create a Telegram bot via @BotFather
2. Add bot to a test group
3. Configure Telegram connection in SolidPing
4. Use "Test Connection" button to verify setup
5. Trigger an incident and verify notification arrives
6. Resolve the incident and verify resolution notification
