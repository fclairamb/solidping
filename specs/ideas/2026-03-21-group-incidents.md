# Group-Based Incident Correlation

## Problem

When a server or network segment goes down, multiple checks fail simultaneously, generating N separate incidents and N notification storms. Users see 5 "incident created" alerts when the real situation is "one outage."

## Proposal

Leverage the existing `check_groups` feature to correlate incidents across related checks. When a check belongs to a group, its incidents are scoped to the group rather than the individual check.

### How it would work

- A group incident is created when the first check in the group triggers an incident
- Subsequent check failures in the same group are attached to the existing group incident as timeline events (per-check detail)
- Notifications fire once for the group incident, not per check
- The group incident resolves only when all checks in the group are healthy
- Checks not in any group keep the current behavior (one incident per check)

### Adaptive resolution interaction

- The group incident reopens if *any* check in the group relapses during the cooldown window
- The adaptive recovery threshold applies per group incident (relapse_count increments when any member relapses)

### Open questions

- Should a group incident resolve when all checks recover, or when a quorum recovers?
- How to handle checks being added/removed from a group while a group incident is active?
- Should the group incident title list affected checks or use the group name?
- What happens when a check belongs to multiple groups?

## Status

Idea -- to be designed after adaptive incident resolution is in production.
