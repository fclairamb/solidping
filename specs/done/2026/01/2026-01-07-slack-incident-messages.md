# Slack Incident Messages: Enhanced Rendering

## Overview

Incident notifications in Slack should provide rich, interactive messages with clear visual hierarchy, action buttons, and threaded updates. This creates a professional user experience similar to established monitoring tools while keeping all incident lifecycle updates connected.

## Key Requirements

1. **Rich Message Formatting**: Use Slack's Block Kit for structured, visually appealing messages
2. **Interactive Actions**: Provide buttons for immediate incident response
3. **Threaded Updates**: Keep incident lifecycle events connected in threads
4. **Status Indicators**: Visual tags for incident state and monitor type
5. **Consistent Branding**: App name and icon for all messages

## Message Types

### 1. Initial Incident Notification

Sent when an incident is created (`incident.created` event).

#### Visual Structure
```
┌─────────────────────────────────────────────────────────┐
│ 🔴 SolidPing APP  11:34 AM                              │
├─────────────────────────────────────────────────────────┤
│ New incident for example.com                            │
│                                                          │
│ ┌─────────────────────────────────────────────────────┐ │
│ │ Monitor: example.com                                 │ │
│ │ Cause: Timeout (no headers received)                 │ │
│ │ Checked URL: GET https://www.example.com/            │ │
│ └─────────────────────────────────────────────────────┘ │
│                                                          │
│ Please acknowledge the incident.                        │
│                                                          │
│ [👍 Acknowledge] [👨 I'm unavailable] [🚨 Escalate]    │
│                                                          │
│ ⚠️ Incident  🔵 Monitor                                 │
│                                                          │
│ Incident started today at 11:34:51 AM                   │
└─────────────────────────────────────────────────────────┘
```

#### Block Kit JSON Structure
```json
{
  "text": "New incident for {check_name}",
  "blocks": [
    {
      "type": "header",
      "text": {
        "type": "plain_text",
        "text": "New incident for {check_name}",
        "emoji": true
      }
    },
    {
      "type": "section",
      "fields": [
        {
          "type": "mrkdwn",
          "text": "*Monitor:*\n{check_name}"
        },
        {
          "type": "mrkdwn",
          "text": "*Cause:*\n{failure_reason}"
        },
        {
          "type": "mrkdwn",
          "text": "*Checked URL:*\n{method} `{url}`"
        }
      ]
    },
    {
      "type": "section",
      "text": {
        "type": "mrkdwn",
        "text": "Please acknowledge the incident."
      }
    },
    {
      "type": "actions",
      "block_id": "incident_actions",
      "elements": [
        {
          "type": "button",
          "text": {
            "type": "plain_text",
            "text": "👍 Acknowledge",
            "emoji": true
          },
          "style": "primary",
          "action_id": "acknowledge_incident",
          "value": "{incident_uid}"
        },
        {
          "type": "button",
          "text": {
            "type": "plain_text",
            "text": "👨 I'm unavailable",
            "emoji": true
          },
          "action_id": "unavailable_incident",
          "value": "{incident_uid}"
        },
        {
          "type": "button",
          "text": {
            "type": "plain_text",
            "text": "🚨 Escalate",
            "emoji": true
          },
          "style": "danger",
          "action_id": "escalate_incident",
          "value": "{incident_uid}"
        }
      ]
    },
    {
      "type": "context",
      "elements": [
        {
          "type": "mrkdwn",
          "text": "⚠️ Incident  🔵 Monitor"
        }
      ]
    },
    {
      "type": "context",
      "elements": [
        {
          "type": "mrkdwn",
          "text": "Incident started {timestamp}"
        }
      ]
    }
  ]
}
```

### 2. Resolution Notification

Sent when an incident is resolved (`incident.resolved` event).

#### Visual Structure
```
┌─────────────────────────────────────────────────────────┐
│ 🔴 SolidPing APP  12:22 PM                              │
├─────────────────────────────────────────────────────────┤
│ Automatically resolved example.com incident             │
│                                                          │
│ ┌─────────────────────────────────────────────────────┐ │
│ │ Monitor: example.com                                 │ │
│ │ Cause: Timeout (no headers received)                 │ │
│ │ Length: 45 minutes                                   │ │
│ │ Checked URL: GET https://www.example.com/            │ │
│ └─────────────────────────────────────────────────────┘ │
│                                                          │
│ ⚠️ Incident  🔵 Monitor                                 │
│                                                          │
│ Incident started today at 11:34:51 AM                   │
└─────────────────────────────────────────────────────────┘
```

#### Block Kit JSON Structure
```json
{
  "text": "Automatically resolved {check_name} incident",
  "blocks": [
    {
      "type": "header",
      "text": {
        "type": "plain_text",
        "text": "✅ Automatically resolved {check_name} incident",
        "emoji": true
      }
    },
    {
      "type": "section",
      "fields": [
        {
          "type": "mrkdwn",
          "text": "*Monitor:*\n{check_name}"
        },
        {
          "type": "mrkdwn",
          "text": "*Cause:*\n{failure_reason}"
        },
        {
          "type": "mrkdwn",
          "text": "*Length:*\n{duration}"
        },
        {
          "type": "mrkdwn",
          "text": "*Checked URL:*\n{method} `{url}`"
        }
      ]
    },
    {
      "type": "context",
      "elements": [
        {
          "type": "mrkdwn",
          "text": "⚠️ Incident  🔵 Monitor"
        }
      ]
    },
    {
      "type": "context",
      "elements": [
        {
          "type": "mrkdwn",
          "text": "Incident started {timestamp}"
        }
      ]
    }
  ]
}
```

### 3. Escalation Notification

Sent when an incident is escalated (`incident.escalated` event).

#### Visual Structure
```
┌─────────────────────────────────────────────────────────┐
│ 🔴 SolidPing APP  11:45 AM                              │
├─────────────────────────────────────────────────────────┤
│ 🚨 Incident escalated: example.com                      │
│                                                          │
│ ┌─────────────────────────────────────────────────────┐ │
│ │ Monitor: example.com                                 │ │
│ │ Failures: 5                                          │ │
│ │ Duration: 11 minutes                                 │ │
│ │ Checked URL: GET https://www.example.com/            │ │
│ └─────────────────────────────────────────────────────┘ │
│                                                          │
│ This incident has exceeded the escalation threshold.    │
│                                                          │
│ [👍 Acknowledge] [🔗 View Details]                      │
│                                                          │
│ 🚨 Escalated  ⚠️ Incident                               │
└─────────────────────────────────────────────────────────┘
```

## Threading Behavior

### Initial Incident (Creates Thread)
1. Post initial incident message to configured channel
2. Store `message_ts` (timestamp) in database as `slack_message_ts`
3. Store `channel_id` in database as `slack_channel_id`
4. This message becomes the thread parent

### Resolution Message (Updates Thread)
Two actions occur:

1. **Update Original Message**: Use `chat.update` API to replace the original message blocks
   - Remove action buttons (no longer needed)
   - Update header to show resolved status
   - Add resolution details (duration)
   - Keep the same `message_ts`

2. **Post Thread Reply**: Use `chat.postMessage` with `thread_ts` parameter
   - Post a summary reply in the thread
   - Example: "✅ Incident resolved after 45 minutes."

This approach provides:
- **Channel view**: Shows both the initial alert AND the resolution status (via edited message)
- **Thread view**: Contains the full conversation and timeline
- **Notification**: Users subscribed to the thread get notified of resolution

### Escalation Message (Thread Reply)
- Post as reply to the original incident thread
- Use `thread_ts` pointing to the original message
- Notifies anyone following the thread

## Database Schema Updates

### Table: `incidents`
Add columns to track Slack message metadata:

```sql
ALTER TABLE incidents ADD COLUMN slack_channel_id TEXT;
ALTER TABLE incidents ADD COLUMN slack_message_ts TEXT;
ALTER TABLE incidents ADD COLUMN slack_thread_ts TEXT;
```

| Column | Description |
|--------|-------------|
| `slack_channel_id` | Slack channel ID where message was posted |
| `slack_message_ts` | Timestamp of the main incident message (thread parent) |
| `slack_thread_ts` | Timestamp of the thread (same as `slack_message_ts` for parent messages) |

**Note**: These fields are nullable. Multiple Slack integrations per org would require a separate `incident_notifications` table (future enhancement).

## Interactive Actions

### Action Handlers

When users click action buttons, Slack sends an `interactive_message` payload to the configured endpoint.

#### 1. Acknowledge Action
- **action_id**: `acknowledge_incident`
- **Handler**: Mark incident as acknowledged in database
- **Response**: Update message to show who acknowledged and when
- **Event**: Create `incident.acknowledged` event

#### 2. I'm Unavailable Action
- **action_id**: `unavailable_incident`
- **Handler**: Log the unavailability
- **Response**: Ephemeral message: "Noted. This incident remains unacknowledged."
- **Event**: Create `incident.unavailable_response` event (optional tracking)

#### 3. Escalate Action
- **action_id**: `escalate_incident`
- **Handler**: Manually trigger escalation
- **Response**: Update message, send escalation notifications
- **Event**: Create `incident.escalated` event (if not already escalated)

### Updated Message After Acknowledgment

```json
{
  "replace_original": true,
  "blocks": [
    // ... same blocks as before ...
    {
      "type": "section",
      "text": {
        "type": "mrkdwn",
        "text": "✅ Acknowledged by *@jane.doe* at 11:36 AM"
      }
    },
    // Remove action buttons, or replace with disabled state
    {
      "type": "context",
      "elements": [
        {
          "type": "mrkdwn",
          "text": "⚠️ Incident  🔵 Monitor  ✅ Acknowledged"
        }
      ]
    }
  ]
}
```

## Data Model Changes

### Events Table
Add new event types for interactive actions:

| Event Type | Description |
|------------|-------------|
| `incident.acknowledged` | User acknowledged via Slack button |
| `incident.unavailable_response` | User indicated unavailability |
| `incident.escalated` | Manual or automatic escalation |

Payload examples:
```json
// incident.acknowledged
{
  "slack_user_id": "U123ABC",
  "slack_user_name": "jane.doe",
  "acknowledged_at": "2026-01-07T11:36:00Z",
  "via": "slack"
}

// incident.unavailable_response
{
  "slack_user_id": "U456DEF",
  "slack_user_name": "john.smith",
  "responded_at": "2026-01-07T11:37:00Z"
}
```

## Implementation Guide

### 1. Message Builder Service
Create a reusable service for building Slack messages:

```go
package slack

type MessageBuilder struct {
    incident *models.Incident
    check    *models.Check
    result   *models.CheckResult
}

func (b *MessageBuilder) BuildIncidentCreated() slack.Message {
    // Build Block Kit JSON for initial incident
}

func (b *MessageBuilder) BuildIncidentResolved() slack.Message {
    // Build Block Kit JSON for resolution
}

func (b *MessageBuilder) BuildIncidentEscalated() slack.Message {
    // Build Block Kit JSON for escalation
}
```

### 2. Notification Sender Updates
Update the notification sender to:
1. Build rich Block Kit messages instead of plain text
2. Store `message_ts` and `channel_id` in database after posting
3. Use `chat.update` for resolution messages
4. Use `thread_ts` for thread replies

```go
func (s *SlackNotifier) SendIncidentCreated(ctx context.Context, incident *Incident) error {
    msg := s.builder.BuildIncidentCreated(incident)

    resp, err := s.client.PostMessage(
        incident.SlackChannelID,
        slack.MsgOptionBlocks(msg.Blocks...),
        slack.MsgOptionText(msg.Text, false),
    )
    if err != nil {
        return err
    }

    // Store message metadata
    incident.SlackMessageTs = resp.Timestamp
    incident.SlackChannelId = resp.Channel
    incident.SlackThreadTs = resp.Timestamp // Parent message
    return s.repo.UpdateIncident(ctx, incident)
}

func (s *SlackNotifier) SendIncidentResolved(ctx context.Context, incident *Incident) error {
    // 1. Update original message
    msg := s.builder.BuildIncidentResolved(incident)
    _, _, _, err := s.client.UpdateMessage(
        incident.SlackChannelID,
        incident.SlackMessageTs,
        slack.MsgOptionBlocks(msg.Blocks...),
    )
    if err != nil {
        return err
    }

    // 2. Post thread reply
    duration := incident.ResolvedAt.Sub(incident.StartedAt)
    replyText := fmt.Sprintf(
        "✅ Incident resolved after %s. All systems operational.",
        formatDuration(duration),
    )
    _, _, err = s.client.PostMessage(
        incident.SlackChannelID,
        slack.MsgOptionText(replyText, false),
        slack.MsgOptionTS(incident.SlackThreadTs), // Reply in thread
    )

    return err
}
```

### 3. Interactive Action Handler
Create endpoint handler for Slack interactive messages:

```go
// POST /api/integrations/slack/interactive
func (h *Handler) HandleInteractive(c *gin.Context) {
    payload := parseSlackPayload(c.Request)

    switch payload.Actions[0].ActionID {
    case "acknowledge_incident":
        h.handleAcknowledge(c, payload)
    case "unavailable_incident":
        h.handleUnavailable(c, payload)
    case "escalate_incident":
        h.handleEscalate(c, payload)
    }
}

func (h *Handler) handleAcknowledge(c *gin.Context, payload *SlackPayload) {
    incidentUID := payload.Actions[0].Value
    slackUserID := payload.User.ID

    // 1. Get incident
    incident, err := h.service.GetIncident(c, incidentUID)

    // 2. Mark as acknowledged
    incident.AcknowledgedAt = time.Now()
    incident.AcknowledgedBy = slackUserID // Map to internal user if needed
    h.service.UpdateIncident(c, incident)

    // 3. Create event
    h.service.CreateEvent(c, &Event{
        IncidentUID: incident.UID,
        EventType:   "incident.acknowledged",
        Payload: map[string]interface{}{
            "slack_user_id":   slackUserID,
            "slack_user_name": payload.User.Username,
            "via":             "slack",
        },
    })

    // 4. Update message to show acknowledgment
    updatedMsg := h.builder.BuildAcknowledgedMessage(incident, payload.User)
    c.JSON(200, updatedMsg) // Slack will update the message
}
```

### 4. Configuration & Scopes

Required Slack OAuth scopes:
- `chat:write` - Post messages
- `chat:write.customize` - Customize app name and icon
- `channels:read` - List channels
- `im:write` - Send DMs
- `users:read` - Get user info for @mentions
- `commands` - Slash commands (optional, for future `/solidping` commands)

## User Experience Flows

### Flow 1: Incident Created → Acknowledged → Resolved
1. Check fails → Incident created
2. Slack message posted with buttons
3. User clicks "👍 Acknowledge"
4. Message updates to show acknowledgment
5. Check recovers → Incident resolved
6. Original message updated to show resolution
7. Thread reply posted: "✅ Incident resolved..."

### Flow 2: Incident Created → Escalated → Resolved
1. Check fails repeatedly
2. Initial Slack message posted
3. Failure count reaches escalation threshold
4. Thread reply posted: "🚨 Incident escalated..."
5. Check recovers
6. Original message updated, thread reply posted

### Flow 3: Unacknowledged Incident
1. Check fails → Incident created
2. Slack message posted
3. No one acknowledges
4. After N minutes, send reminder (future feature)
5. Or escalate automatically based on time (future feature)

## Future Enhancements

### 1. Private Threads for Collaboration
- Allow responders to discuss in the incident thread
- Bot can parse thread messages for commands like "I'm investigating" or "Root cause: ..."

### 2. Incident Timeline in Thread
- Post significant events as thread replies:
  - "3rd consecutive failure"
  - "Escalation triggered"
  - "Jane acknowledged the incident"
  - "Incident resolved"

### 3. On-Call Rotation Integration
- Automatically @mention the current on-call engineer
- Show who's on-call in the message

### 4. Incident Status Command
- `/solidping status inc_abc123` in thread to get current status
- Reply with metrics, duration, last check result

### 5. Quick Actions in Thread
- Reply "ack" to acknowledge
- Reply "escalate" to escalate
- Reply "snooze 30m" to pause notifications

## Testing Checklist

- [ ] Initial incident message renders correctly with all fields
- [ ] Action buttons are clickable and functional
- [ ] Acknowledgment updates the message and creates event
- [ ] Resolution updates the original message (not creates new one)
- [ ] Resolution creates a thread reply
- [ ] Thread replies appear in the correct thread
- [ ] Multiple incidents don't interfere with each other
- [ ] Escalation posts to the correct thread
- [ ] Message timestamps are formatted correctly
- [ ] Channel mentions and user mentions work
- [ ] Messages work in both public and private channels
- [ ] Interactive actions work for different users
- [ ] Database correctly stores and retrieves message metadata

---

**Status**: Draft | **Created**: 2026-01-07 | **Related**: `2025-12-26-incidents.md`, `2026-01-04-slack-integration.md`
