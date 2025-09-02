# Discord Notification Integration

## Overview

This spec defines Discord as a notification platform for SolidPing, mirroring the existing Slack integration (`back/internal/integrations/slack/`). The Discord bot sends rich incident notifications with interactive buttons, supports thread-based incident tracking, and offers bot commands for managing checks directly from Discord.

The existing Slack integration spec (`specs/past/2026/01/2026-01-04-slack-integration.md`) already planned for Discord compatibility — this spec makes it concrete.

## Concept Mapping

| Concept | Slack | Discord |
|---------|-------|---------|
| Workspace | Workspace (Team) | Server (Guild) |
| Channel | Channel | Text Channel |
| App | Slack App | Discord Bot |
| Mentions | `@solidping` | `@SolidPing` |
| Rich messages | Block Kit | Embeds |
| Buttons | Interactive messages | Message Components |
| Threads | Message threads | Forum/thread channels |
| Events | Events API (HTTP) | Gateway (WebSocket) or Interactions endpoint (HTTP) |
| Scopes | OAuth scopes | Bot permissions (intents) |

## Data Model

### Connection Type

Add to `models/integration.go`:

```go
ConnectionTypeDiscord ConnectionType = "discord"
```

### Discord Settings (JSONB)

Stored in `IntegrationConnection.settings`:

```go
type DiscordSettings struct {
    GuildID     string `json:"guildId"`
    GuildName   string `json:"guildName"`
    BotUserID   string `json:"botUserId"`
    ChannelID   string `json:"channelId,omitempty"`   // Default notification channel
    ChannelName string `json:"channelName,omitempty"` // Default channel name
}
```

Note: The bot token is stored in server config (`SP_DISCORD_BOT_TOKEN`), not per-connection, since one bot serves all guilds.

### Check-level Override

Same pattern as Slack — `CheckConnection.settings` can override `channelId`:

```json
{"channelId": "123456789012345678", "channelName": "#critical-alerts"}
```

## Notification Sender

### Registration

Add to `notifications/registry.go`:

```go
case models.ConnectionTypeDiscord:
    return &DiscordSender{}, true
```

### DiscordSender

New file `notifications/discord.go` implementing `Sender` interface:

```go
type DiscordSender struct{}

func (s *DiscordSender) Send(ctx context.Context, jctx *jobdef.JobContext, payload *Payload) error {
    settings := resolveDiscordSettings(payload)
    if settings.ChannelID == "" {
        return fmt.Errorf("no Discord channel configured")
    }

    client := discord.NewClient(jctx.Config.Discord.BotToken)

    switch payload.EventType {
    case "incident.created":
        return s.sendIncidentCreated(ctx, client, settings, payload)
    case "incident.resolved":
        return s.sendIncidentResolved(ctx, client, settings, payload)
    case "incident.escalated":
        return s.sendIncidentEscalated(ctx, client, settings, payload)
    case "incident.reopened":
        return s.sendIncidentReopened(ctx, client, settings, payload)
    default:
        return fmt.Errorf("unknown event type: %s", payload.EventType)
    }
}
```

## Discord API Client

New package `back/internal/integrations/discord/`:

### client.go

Lightweight HTTP client wrapping the Discord REST API (no need for a full gateway library for notifications):

```go
type Client struct {
    botToken   string
    httpClient *http.Client
    baseURL    string // https://discord.com/api/v10
}

func NewClient(botToken string) *Client

// Channel messages
func (c *Client) SendMessage(ctx context.Context, channelID string, msg *MessageCreate) (*Message, error)
func (c *Client) EditMessage(ctx context.Context, channelID, messageID string, msg *MessageEdit) (*Message, error)

// Threads
func (c *Client) CreateThread(ctx context.Context, channelID, messageID string, name string) (*Channel, error)
func (c *Client) SendThreadMessage(ctx context.Context, threadID string, msg *MessageCreate) (*Message, error)

// Channels
func (c *Client) ListGuildChannels(ctx context.Context, guildID string) ([]Channel, error)
```

### types.go

Discord API types:

```go
type MessageCreate struct {
    Content    string      `json:"content,omitempty"`
    Embeds     []Embed     `json:"embeds,omitempty"`
    Components []Component `json:"components,omitempty"`
}

type Embed struct {
    Title       string  `json:"title,omitempty"`
    Description string  `json:"description,omitempty"`
    Color       int     `json:"color,omitempty"` // Decimal color (red=16711680, green=65280, orange=16744448)
    Fields      []Field `json:"fields,omitempty"`
    Timestamp   string  `json:"timestamp,omitempty"` // ISO8601
    Footer      *Footer `json:"footer,omitempty"`
}

type Field struct {
    Name   string `json:"name"`
    Value  string `json:"value"`
    Inline bool   `json:"inline,omitempty"`
}

type Component struct {
    Type       int         `json:"type"`       // 1=ActionRow, 2=Button
    Components []Component `json:"components,omitempty"` // For ActionRow
    Style      int         `json:"style,omitempty"`      // 1=Primary, 2=Secondary, 3=Success, 4=Danger
    Label      string      `json:"label,omitempty"`
    CustomID   string      `json:"custom_id,omitempty"`
    Emoji      *Emoji      `json:"emoji,omitempty"`
}
```

## Incident Notification Messages

### incident.created

Rich embed with action buttons:

```
┌─────────────────────────────────────────────┐
│ 🔴 Incident: api-example-com is DOWN        │ (red embed, color: 16711680)
│                                             │
│ Check:     api-example-com                  │
│ Status:    DOWN                             │
│ Started:   2026-03-22 14:30 UTC             │
│ Duration:  Just now                         │
│                                             │
│ Error: Connection timeout after 30s         │
│                                             │
│ [Acknowledge] [Escalate] [I'm unavailable]  │ (buttons)
└─────────────────────────────────────────────┘
```

**Embed JSON:**
```json
{
  "embeds": [{
    "title": "🔴 Incident: api-example-com is DOWN",
    "color": 16711680,
    "fields": [
      {"name": "Check", "value": "api-example-com", "inline": true},
      {"name": "Status", "value": "DOWN", "inline": true},
      {"name": "Started", "value": "<t:1711111800:R>", "inline": true},
      {"name": "Error", "value": "Connection timeout after 30s"}
    ],
    "timestamp": "2026-03-22T14:30:00Z"
  }],
  "components": [{
    "type": 1,
    "components": [
      {"type": 2, "style": 1, "label": "Acknowledge", "custom_id": "ack:incident_uid"},
      {"type": 2, "style": 4, "label": "Escalate", "custom_id": "escalate:incident_uid"},
      {"type": 2, "style": 2, "label": "I'm unavailable", "custom_id": "unavailable:incident_uid"}
    ]
  }]
}
```

### incident.resolved

Updates original message + posts thread reply:

```
┌─────────────────────────────────────────────┐
│ ✅ Resolved: api-example-com is UP          │ (green embed, color: 65280)
│                                             │
│ Check:     api-example-com                  │
│ Duration:  5m 23s                           │
│ Resolved:  2026-03-22 14:35 UTC             │
└─────────────────────────────────────────────┘
```

### incident.escalated

```
┌─────────────────────────────────────────────┐
│ ⚠️ Escalated: api-example-com               │ (orange embed, color: 16744448)
│                                             │
│ Check:     api-example-com                  │
│ Duration:  15m (threshold: 10m)             │
│ Status:    Still DOWN                       │
└─────────────────────────────────────────────┘
```

## Interactive Buttons (Interactions Endpoint)

Discord sends button clicks as HTTP POST to an interactions endpoint.

### Interactions Handler

```
POST /api/v1/integrations/discord/interactions
```

Discord requires:
1. **Signature verification**: Verify `X-Signature-Ed25519` and `X-Signature-Timestamp` headers using the application's public key
2. **Ping response**: Respond to `type: 1` (PING) with `{"type": 1}` for Discord's URL validation

**Button actions:**

| custom_id prefix | Action |
|-----------------|--------|
| `ack:{incidentUid}` | Acknowledge incident, update embed to show who acknowledged |
| `escalate:{incidentUid}` | Mark as escalated, notify escalation contacts |
| `unavailable:{incidentUid}` | Mark user as unavailable, try next responder |

### Response

Button interactions respond with a message update (type 7) or a new message (type 4):

```json
{
  "type": 7,
  "data": {
    "embeds": [{"title": "✅ Acknowledged by @Nelly", "color": 65280}],
    "components": []
  }
}
```

## Thread-Based Incident Tracking

Store the Discord message ID in `state_entries` to enable thread replies:

```go
// On incident.created, store the message ID
stateEntry := &models.StateEntry{
    Key:   fmt.Sprintf("discord_thread:%s", incident.UID),
    Value: models.JSONMap{"messageId": message.ID, "channelId": channelID},
}

// On incident.resolved/escalated, create thread or reply
existing, _ := db.GetStateEntry(ctx, fmt.Sprintf("discord_thread:%s", incident.UID))
if existing != nil {
    messageID := existing.Value["messageId"].(string)
    // Create a thread from the original message
    thread, _ := client.CreateThread(ctx, channelID, messageID, "Incident: "+check.Name)
    client.SendThreadMessage(ctx, thread.ID, resolvedMsg)
}
```

## Bot Commands (Optional — Phase 2)

For "pure Discord mode", the bot responds to mentions. This requires the Gateway WebSocket connection or slash commands.

**Recommended: Use Slash Commands** (simpler than gateway, HTTP-based):

Register application commands via Discord API:

| Command | Description |
|---------|-------------|
| `/solidping create <url>` | Create a new HTTP check |
| `/solidping list` | List all checks with status |
| `/solidping incidents` | Show active incidents |
| `/solidping ack <incident>` | Acknowledge an incident |
| `/solidping config default-channel` | Set current channel as default |
| `/solidping login` | Get a link to the web dashboard |

Slash commands are delivered to the same interactions endpoint (`/api/v1/integrations/discord/interactions`).

## Bot Installation Flow

### OAuth2 Bot Authorization

Bot is added to a guild via OAuth2 with bot scope:

```
https://discord.com/oauth2/authorize?
  client_id={CLIENT_ID}&
  scope=bot+applications.commands&
  permissions=2048&
  guild_id={GUILD_ID}
```

**Permissions needed:** `2048` = Send Messages. Full permission integer:
- Send Messages (2048)
- Send Messages in Threads (274877906944)
- Create Public Threads (34359738368)
- Embed Links (16384)
- Use External Emojis (262144)
- Add Reactions (64)

**Total:** `309237981248`

### Installation Callback

```
POST /api/v1/integrations/discord/install
```

After bot is added to a guild:
1. Create/update `IntegrationConnection` with `type: "discord"`
2. Store `DiscordSettings` with `guildId` and `guildName`
3. Bot automatically receives `GUILD_CREATE` event (if using gateway)

### Channel Auto-Configuration

Same pattern as Slack:
1. Bot is invited to a channel (or granted channel permissions)
2. First channel where the bot can send messages becomes the default
3. Bot sends welcome message: "I'll send notifications here by default. Use `/solidping config default-channel` in another channel to change this."

For web-app mode: UI dropdown to select channel (calls `ListGuildChannels` API).

## Configuration

Shares config with the OAuth spec:

```go
type DiscordConfig struct {
    ClientID       string `koanf:"client_id"`
    ClientSecret   string `koanf:"client_secret"`
    BotToken       string `koanf:"bot_token"`
    PublicKey      string `koanf:"public_key"`    // For interaction signature verification
    RedirectURL    string `koanf:"redirect_url"`
}
```

**Environment variables:**
```bash
SP_DISCORD_CLIENT_ID=123456789012345678
SP_DISCORD_CLIENT_SECRET=abcdef...
SP_DISCORD_BOT_TOKEN=MTIz...
SP_DISCORD_PUBLIC_KEY=abc123...  # From Discord app settings, for verifying interactions
SP_DISCORD_REDIRECT_URL=https://app.solidping.com/api/v1/auth/discord/callback
```

## Files to Create/Modify

### New Files
- `back/internal/integrations/discord/client.go` — Discord REST API client
- `back/internal/integrations/discord/types.go` — Discord API type definitions
- `back/internal/integrations/discord/service.go` — Integration business logic (install, channel management)
- `back/internal/integrations/discord/handler.go` — HTTP handlers (install callback, interactions endpoint)
- `back/internal/integrations/discord/verify.go` — Ed25519 signature verification
- `back/internal/integrations/discord/interactions.go` — Button/command interaction handlers
- `back/internal/notifications/discord.go` — DiscordSender implementing Sender interface

### Modified Files
- `back/internal/notifications/registry.go` — Register `DiscordSender`
- `back/internal/db/models/integration.go` — Add `ConnectionTypeDiscord`
- `back/internal/config/config.go` — Add `Discord DiscordConfig` field (if not done in OAuth spec)
- `back/internal/app/server.go` — Register Discord integration routes
- `apps/dash0/` — Add Discord integration UI (install button, channel selection, connection settings)

## Implementation Priority

### Phase 1: Notifications (Core)
- [ ] Discord REST API client (send/edit messages, list channels)
- [ ] `ConnectionTypeDiscord` constant
- [ ] `DiscordSender` implementing `Sender` interface
- [ ] Rich embed message builders for incident lifecycle
- [ ] Register in notification registry
- [ ] Ed25519 signature verification for interactions
- [ ] Interactions endpoint with button handling (ack, escalate, unavailable)
- [ ] Thread-based incident tracking via state_entries
- [ ] Bot installation flow (OAuth2 bot authorization)

### Phase 2: Channel Management & UI
- [ ] Channel auto-configuration on bot join
- [ ] Welcome message on first channel
- [ ] Default channel management (API + UI)
- [ ] Per-check channel override
- [ ] Frontend: Discord integration settings page
- [ ] Frontend: Channel selector dropdown

### Phase 3: Bot Commands (Slash Commands)
- [ ] Register slash commands with Discord API
- [ ] `/solidping create`, `/solidping list`, `/solidping incidents`
- [ ] `/solidping ack`, `/solidping config`
- [ ] `/solidping login` for web access

## Security Considerations

1. **Interaction verification**: All interactions must be verified with Ed25519 signature (Discord requirement, app will be disabled if not verified)
2. **Bot token**: Stored server-side only, never exposed to frontend
3. **Permission minimization**: Request only necessary bot permissions
4. **Rate limiting**: Discord has strict rate limits (50 requests/second per route) — implement retry with `Retry-After` header
5. **Guild verification**: Verify the guild in interaction payloads matches a known IntegrationConnection

---

Status: Draft
Created: 2026-03-22
