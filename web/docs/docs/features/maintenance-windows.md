---
sidebar_position: 6
title: Maintenance Windows
---

# Maintenance Windows

Maintenance windows let you **suppress alerts during planned work** — deployments, upgrades, infrastructure changes — so your team isn't paged for downtime you already know about. While a window is active, failing checks in scope do not create incidents or send notifications.

## Defining a Window

| Setting | Description |
|---------|-------------|
| Title / Description | What the maintenance is for |
| Start At / End At | When the window opens and closes |
| Recurrence | `none`, `daily`, `weekly`, or `monthly` |
| Recurrence End | Optional date after which the recurrence stops |

A one-time window uses `recurrence: none`. Recurring windows repeat the same time-of-day slot on the chosen cadence until the recurrence end (or indefinitely).

## Scope

A maintenance window can apply to:

- **Specific checks** — only the listed checks are suppressed
- **Check groups** — every check in the group is suppressed
- **The whole organization** — when no checks or groups are attached, all checks are suppressed

## Behavior

When a check fails while a window covering it is active, SolidPing **skips incident processing entirely** — no incident is opened, escalated, or notified. Results are still recorded, so your history and uptime metrics remain accurate; you simply won't be paged.

Windows can be listed by status (`active`, `upcoming`, `past`) from the dashboard or API, making it easy to see what is currently silenced and what is scheduled.
