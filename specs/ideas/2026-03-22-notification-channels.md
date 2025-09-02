# Notification Channels: Discord, Teams, Telegram, PagerDuty

## Overview

SolidPing currently supports only 3 notification channels (Email, Slack, Webhooks). All major competitors offer 12-90+ channels. Adding Discord, Microsoft Teams, Telegram, and PagerDuty covers the most requested integrations across the industry.

Telegram already has a dedicated spec: `specs/next/2026-03-22-telegram-notifications.md`.

## Goals

1. Add Discord, Microsoft Teams, and PagerDuty as notification channels
2. Follow the same connection-based architecture as Slack and Webhooks
3. Support all incident event types: `incident.created`, `incident.resolved`, `incident.escalated`, `incident.reopened`

---

## Discord

Connection type: `discord`

### Settings (JSONB)

```json
{
  "webhook_url": "https://discord.com/api/webhooks/1234567890/abcdef..."
}
```

### Implementation

- Uses Discord webhook API (no bot/OAuth needed)
- POST JSON to webhook URL with embed format
- Color-coded embeds: red (created), green (resolved), orange (escalated), yellow (reopened)
- Fields: check name, status, duration, region, link to dashboard

### Competitor Reference

BetterStack, UptimeRobot, StatusCake, Checkly, Healthchecks.io, Uptime Kuma, Gatus — all support Discord.

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

- Each channel is a new sender in `back/internal/notifications/`
- Each channel registers as a connection type in the connection system
- Per-check overrides (e.g., channel selection) follow existing Slack pattern
- All channels share the same incident event payload structure
- Dashboard UI: add icons and setup forms for each new connection type

## Priority

Discord and Teams are the simplest (webhook-only, no OAuth). PagerDuty requires Events API v2 integration but is critical for DevOps teams. Telegram is covered by its own spec.

**Suggested order**: Telegram → Discord → Teams → PagerDuty
