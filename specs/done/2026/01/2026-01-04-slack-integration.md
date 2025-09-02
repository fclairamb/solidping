# Slack Integration Design

## Questions

What do you think is the best way to handle the slack integration ?

I'd like to support two similar but somehow independent modes:
- Pure slack mode - You add the app and do everything through slack. If you ever need to access the web app
  you will use the slack authentication.
- Web app mode - You do everything through the app but you can still use the slack integration to receive and send notifications.

I'd like to define how I should add the app to the slack workspace.
- For the pure-slack mode, should I ask for a webhook definition to have a channel name like betterstack uptime does or should I created
  a new channel called #solidping. Or Should I wait to be invited into a channel and to receive a command like "@solidping set default channel".
- For the web app mode, should I ask to select a channel in the UI or should I apply the sames rules ?

I'd like to make it simple to use and but also simple to maintain.

And I'd rather keep the same logic for upcoming integrations like discord.

---

## Analysis

### Channel Configuration Approaches

Let me evaluate each approach:

| Approach | Pros | Cons |
|----------|------|------|
| **Webhook-based** (BetterStack style) | Explicit channel choice, clear intent | Extra setup steps, webhooks being deprecated by Slack |
| **Create #solidping channel** | Consistent, easy to find | May not fit team workflow, channel clutter |
| **Wait for invite + command** | Flexible, natural Slack UX | Onboarding friction, unclear "not configured" state |
| **Select during OAuth** | Immediate setup | Requires `channels:read` scope, long channel lists |

### The Problem with Forcing Early Channel Selection

Asking for a channel during installation creates friction:
1. User may not know which channel they want yet
2. They might want different channels for different checks
3. It couples installation with configuration

### The Problem with Webhooks

Slack is deprecating incoming webhooks in favor of apps. More importantly, webhooks are:
- One-way (can't receive commands)
- Per-channel (need multiple for multiple channels)
- Extra configuration burden on users

---

## Recommendation: Auto-Configure on First Invite

**Core principle: Zero configuration required. First channel becomes the default.**

### Installation Flow

```
1. User clicks "Add to Slack"
2. OAuth completes → App installed to workspace
3. User invites bot to a channel (e.g., /invite @solidping in #alerts)
4. Bot receives `member_joined_channel` event
5. If no default channel set → This channel becomes the default
6. Bot sends welcome message: "I'll send notifications here by default.
   Change anytime with @solidping config default-channel #other"
```

### Channel Configuration (Same for Both Modes)

Channels are configured at two levels:

1. **Organization default** (auto-configured on first invite, changeable)
   - Auto: First channel the bot is invited to
   - Web UI: Settings → Integrations → Slack → Default Channel
   - Slack: `@solidping config default-channel #alerts`

2. **Per-check override** (optional)
   - Web UI: Check settings → Notification channel
   - Slack: `@solidping check my-check --channel #critical-alerts`

### Why This Works for Both Modes

| Mode | Channel Config Method | Works? |
|------|----------------------|--------|
| Pure Slack | Invite bot to channel → auto-configured | Yes |
| Pure Slack | `@solidping config default-channel #alerts` | Yes |
| Web App | UI dropdown to select channel | Yes |
| Web App | Same Slack mentions still work | Yes |

### Notification Behavior

```
When sending notification for a check:
  1. Get all CheckConnections for this check
  2. For each CheckConnection:
     a. Get the IntegrationConnection
     b. Merge settings: effective = connection.settings + checkConnection.settings
     c. Resolve target (channel/recipients/url) from effective settings
     d. If no target configured → Skip (log warning)
     e. Send notification to resolved target
```

**Target Resolution (per integration type):**

| Type | Resolution |
|------|------------|
| Slack | `effective.channel_id` ?? skip |
| Discord | `effective.channel_id` ?? skip |
| Email | `effective.recipients` ?? skip |
| Webhook | `effective.url` ?? skip |

This is explicit and predictable. No surprises.

---

## Discord Compatibility

This pattern maps perfectly to Discord:

| Concept | Slack | Discord |
|---------|-------|---------|
| Workspace | Workspace | Server (Guild) |
| Channel | Channel | Channel |
| App | Slack App | Discord Bot |
| Mentions | `@solidping` | `@SolidPing` |

Same configuration flow:
1. Install bot to server
2. Invite bot to a channel → becomes default
3. Optionally change default or override per-check

---

## Pure Slack Mode: Complete UX

For users who never want to touch the web UI:

### Initial Setup
```
1. Add SolidPing to Slack
2. Invite bot to a channel: /invite @solidping
   → Bot joins and sends: "I'll send notifications here by default.
      Change anytime with @solidping config default-channel #other"
3. Done - org created, user created, default channel set
```

### Daily Usage
```
@solidping create https://api.example.com
  → Creates check "api-example-com", notifications to default channel

@solidping list
  → Shows all checks with status

@solidping check api-example-com --channel #api-alerts
  → Override notification channel for this check

@solidping pause api-example-com
@solidping resume api-example-com

@solidping incidents
  → Show active incidents

@solidping ack abc123
  → Acknowledge incident
```

### Accessing Web UI (When Needed)
```
@solidping login
  → "Click here to access the web dashboard" (Slack OAuth link)
```

---

## Web App Mode: Complete UX

For users who prefer the web UI:

### Initial Setup
```
1. Sign up on web app (email/password or any OAuth)
2. Go to Settings → Integrations → Add Slack
3. OAuth flow completes
4. Invite bot to a channel → becomes default (or select in UI)
```

### Daily Usage
- Create/manage checks in web UI
- Set notification channels per-check or use default
- Slack mentions (`@solidping ...`) still work as shortcuts

---

## Data Model

### IntegrationConnection (existing)

Stores the connection to an external service at the organization level.

```
IntegrationConnection
├── uid
├── organization_uid
├── type: "slack" | "discord" | "email" | "webhook"
├── name: string
├── enabled: bool
├── is_default: bool
├── settings: JSONB {
│     // Type-specific settings with defaults
│   }
├── created_at, updated_at, deleted_at
```

**Settings by type:**

| Type | Settings |
|------|----------|
| Slack | `team_id`, `team_name`, `bot_user_id`, `access_token`, `channel_id`, `channel_name`, `scopes` |
| Discord | `guild_id`, `guild_name`, `bot_token`, `channel_id`, `channel_name` |
| Email | `smtp_host`, `smtp_port`, `from_address`, `recipients` |
| Webhook | `url`, `headers`, `method` |

### CheckConnection (existing - needs settings field)

Links a Check to an IntegrationConnection with optional setting overrides.

```
CheckConnection
├── uid
├── check_uid
├── connection_uid
├── organization_uid
├── settings: JSONB (nullable)  ← NEW: optional overrides
├── created_at
├── updated_at                  ← NEW: track changes
```

**Override settings by type:**

| Type | Override Settings Example |
|------|---------------------------|
| Slack | `{"channel_id": "C123ABC", "channel_name": "#critical-alerts"}` |
| Discord | `{"channel_id": "123456789", "channel_name": "#alerts"}` |
| Email | `{"recipients": ["oncall@example.com", "team@example.com"]}` |
| Webhook | `{"url": "https://custom-endpoint.com/critical", "headers": {"X-Priority": "high"}}` |

### Effective Settings Resolution

When sending a notification, merge connection defaults with check-level overrides:

```
func GetEffectiveSettings(connection, checkConnection):
    effective = connection.settings.clone()

    if checkConnection.settings != nil:
        for key, value in checkConnection.settings:
            effective[key] = value  // Override wins

    return effective
```

**Example flow for Slack:**
```
IntegrationConnection.settings = {
    "team_id": "T123",
    "channel_id": "C_GENERAL",
    "channel_name": "#general"
}

CheckConnection.settings = {
    "channel_id": "C_ALERTS",
    "channel_name": "#alerts"
}

Effective = {
    "team_id": "T123",
    "channel_id": "C_ALERTS",      // overridden
    "channel_name": "#alerts"      // overridden
}

→ Notification sent to #alerts
```

---

## Implementation Priority

### Phase 1: Data Model & Foundation
- [ ] Add `settings` JSONB column to `check_connections` table
- [ ] Add `updated_at` column to `check_connections` table
- [ ] Update `CheckConnection` Go model with Settings field
- [ ] Add `CheckConnectionUpdate` model for partial updates
- [ ] Update DB service interface with update method
- [ ] Update PostgreSQL/SQLite implementations
- [ ] Update check-connections API to accept/return settings
- [ ] Implement `GetEffectiveSettings()` helper function

### Phase 2: Auto-Configure & Channel Management
- [ ] Handle `member_joined_channel` event to auto-set default channel
- [ ] Send welcome message on first channel join
- [ ] `@solidping config default-channel` command (app_mention)
- [ ] Default channel UI in web app (Settings → Integrations → Slack)
- [ ] Channel resolution in notification sender

### Phase 3: Pure Slack Mode (app_mention)
- [ ] `@solidping create <url>` to create checks
- [ ] `@solidping list`, `@solidping incidents`
- [ ] `@solidping check <name> --channel #channel` for per-check override
- [ ] `@solidping pause/resume <check>`
- [ ] `@solidping ack <incident>` for incident acknowledgment
- [ ] Interactive messages for incident management

### Phase 4: Refinement
- [ ] `@solidping login` for web access
- [ ] Per-check channel override in web UI (Check settings)
- [ ] Channel selector dropdown in web UI (fetched from Slack API)

---

## Summary

**Key decisions:**

1. **First channel = default channel** - Zero config, invite bot → done
2. **App mentions (`@solidping`) over slash commands** - Simpler implementation, natural Slack UX
3. **Support org default + per-check override** - Flexible without complexity
4. **Same pattern for all integrations** - One mental model, one codebase pattern
5. **Generic `settings JSONB` on CheckConnection** - Extensible overrides for any integration type
6. **Web mode uses UI dropdowns** - Expected web UX

**Data model approach:**

- `IntegrationConnection.settings` stores type-specific defaults (channel, recipients, URL, etc.)
- `CheckConnection.settings` stores optional overrides (nullable JSONB)
- Effective settings = merge(connection.settings, checkConnection.settings)
- Same key names everywhere (`channel_id`, not `default_channel_id`) for simple merging

This approach is:
- **Zero-config for users**: Invite bot to channel → ready to receive notifications
- **Simple to maintain**: One configuration pattern for all integrations
- **Flexible**: Works for both modes without mode-specific code
- **Extensible**: New integration types just define their settings schema, no schema migrations needed
