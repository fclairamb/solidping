# Notification Channels: Teams, Telegram, PagerDuty

## Overview

SolidPing has 9 native notification channels today (Slack, Discord, Email, Webhooks, Google Chat, Mattermost, Ntfy, Opsgenie, Pushover). Major competitors offer 12-90+ channels. Adding Microsoft Teams, Telegram, and PagerDuty covers the most-requested remaining integrations and brings parity with BetterStack on the channels DevOps teams expect.

Telegram has its own dedicated spec: `specs/ideas/2026-03-22-telegram-notifications.md`. Discord shipped earlier (`specs/done/2026/03/2026-03-22-discord-integration.md`) and is the reference for the webhook + embed pattern below.

## Goals

1. Add Microsoft Teams, Telegram, and PagerDuty as notification channels
2. Follow the same connection-based architecture as Slack, Discord, and Webhooks
3. Support all incident event types: `incident.created`, `incident.resolved`, `incident.escalated`, `incident.reopened`
4. Respect group-incident correlation: each connection fires once per group event, not once per member

---

## Microsoft Teams

Connection type: `msteams`

### Settings (JSONB)

```json
{
  "webhook_url": "https://outlook.office.com/webhook/..."
}
```

### Implementation

- Uses Teams Incoming Webhook connector
- POST Adaptive Card JSON to webhook URL
- Color-coded cards with incident details
- Action buttons linking to dashboard

### Competitor Reference

BetterStack, UptimeRobot, Pingdom, StatusCake, Checkly, Healthchecks.io, Uptime Kuma, Gatus — all support Teams.

---

## PagerDuty

Connection type: `pagerduty`

### Settings (JSONB)

```json
{
  "routing_key": "R0...",
  "severity": "critical"
}
```

### Implementation

- Uses PagerDuty Events API v2 (`https://events.pagerduty.com/v2/enqueue`)
- `trigger` event on incident creation/escalation
- `resolve` event on incident resolution
- Maps SolidPing incident UID to PagerDuty `dedup_key` for correlation
- Configurable severity: `critical`, `error`, `warning`, `info`

### Competitor Reference

BetterStack, UptimeRobot, Pingdom, StatusCake, Checkly, Healthchecks.io, Uptime Kuma, Gatus — all support PagerDuty.

---

## Implementation Notes

- Each channel is a new sender in `server/internal/notifications/` (registered in `registry.go`)
- Each channel registers as a connection type in `server/internal/db/models/integration.go`
- Per-check overrides (e.g., channel selection) follow the existing Slack/Discord pattern
- All channels share the same incident event payload structure and respect group-incident correlation
- Dashboard UI: add icons and setup forms for each new connection type, plus i18n keys

## Priority

Teams is the simplest (incoming webhook + adaptive card). PagerDuty requires Events API v2 integration but is critical for DevOps teams. Telegram is covered by its own spec and uses bot token + chat ID.

**Suggested order**: Telegram → Teams → PagerDuty
