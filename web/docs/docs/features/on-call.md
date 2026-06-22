---
sidebar_position: 5
title: On-Call & Escalation
---

# On-Call & Escalation

SolidPing can route incidents to the right person at the right time using **on-call schedules** and **escalation policies**. Together they answer two questions: *who is responsible right now?* and *what happens if they don't respond?*

## On-Call Schedules

A schedule defines a rotation of users who take turns being on call.

| Setting | Description |
|---------|-------------|
| Name / Slug | Schedule identity |
| Timezone | Local timezone used for handoff calculations (DST-safe) |
| Rotation Type | `daily` or `weekly` |
| Handoff Time | Wall-clock time of day the rotation hands off (`HH:MM`) |
| Handoff Weekday | Day of week for weekly rotations |
| Start At | Anchor point for the first handoff |
| Roster | Ordered list of users in the rotation |

At any moment, the schedule resolves to exactly one on-call user by walking the rotation from the start anchor.

### Overrides

Need someone to cover a shift? Add a time-bounded **override** that temporarily replaces the scheduled user between a start and end time, with an optional reason. Overrides take precedence over the normal rotation.

### iCal Feed

Each schedule can publish a private **iCal feed**, so on-call shifts show up in Google Calendar, Apple Calendar, or Outlook. The feed token can be enabled, disabled, or rotated at any time.

## Escalation Policies

An escalation policy is an ordered list of **steps**. When an incident fires, the policy walks down the steps, waiting between each, until the incident is acknowledged or the policy is exhausted.

### Steps and Targets

Each step waits a configurable delay, then notifies one or more **targets** in parallel. A target can be:

| Target Type | Notifies |
|-------------|----------|
| `user` | A specific user |
| `schedule` | Whoever is currently on call for an on-call schedule |
| `connection` | A notification connection (Slack channel, webhook, etc.) |
| `all_admins` | Every administrator in the organization |

### Repeats

A policy can **repeat** its whole sequence a configurable number of times, with a delay between repeats — useful for "keep paging until someone acknowledges" behavior.

## How It Fits Together

```
Incident created
      │
      ▼
Escalation policy step 1  → notify on-call user (via schedule)
      │ (wait, no ack)
      ▼
Escalation policy step 2  → notify backup user + Slack channel
      │ (wait, no ack)
      ▼
Escalation policy step 3  → notify all admins
      │
   (repeat?)
```

Acknowledging the incident stops the escalation. See [Incidents](/features/incidents#acknowledge-snooze-resolve) for acknowledgment, snooze, and resolution.
