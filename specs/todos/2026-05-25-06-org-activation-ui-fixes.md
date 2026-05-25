# Org Activation UI Fixes

## Context

The org activation/onboarding page at `/dash0/orgs/:org` has two small issues.

## Issues

### 1. Double arrow on "View all events" button

The button renders as `View all events → →` (two right-arrow icons) instead of a single `→`. Remove the duplicate arrow so it reads `View all events →`.

### 2. Missing event descriptions

The following activation events lack a human-readable description:

| Event key | Suggested description |
|---|---|
| `org.activation.first_check_created` | Your first uptime check is live and monitoring. |
| `org.activation.first_notification_configured` | You have set up your first notification channel. |

Add descriptions so they appear in the activation checklist UI alongside the event name.
