---
sidebar_position: 12
title: Region Placement
---

# Region placement

Every check runs from one or more regions. A check is placed in one of two
ways:

| | Pinned | Automatic |
|---|---|---|
| Who chooses the regions | You, explicitly | SolidPing |
| When a region goes offline | The check stops getting results and shows **No data** | The check is moved to a healthy region within a minute |
| Private locations | Yes | Never |

## Automatic placement (the default)

A new check with no region list is placed **automatically** on **2 regions**
(`regionCount`). SolidPing considers the regions in this order:

1. your organization's default regions;
2. the platform's default regions;
3. every other region;

and keeps only the ones that:

- are in the check's `regionPool`, when you set one (empty means any cloud region);
- can run the check: a `browser` check needs a region with headless Chrome, an
  `ipVersion: ipv6` target a region with IPv6 egress;
- are healthy right now (a live worker, no ongoing outage).

It then takes the first N. The same inputs always give the same placement.

`regionCount` is reduced, with a warning in the response and in the form, when
fewer regions can run the check, or when your plan's checks-per-minute limit
only fits fewer runs per period (each region runs the check once per period).

### When a region goes offline

SolidPing evaluates every region once a minute. When a region has checks
assigned and no live worker (about 5 minutes after its last worker was seen),
every automatically placed check running there is moved:

- to the next healthy region in its own order that it does not already use;
- its new run is due immediately;
- a **Check Moved to Another Region** event (`check.placement_changed`) records
  `from`, `to` and the reason, and shows on the check page as its placement
  history.

A check with no healthy region left keeps its placement and shows **No data**,
exactly like a pinned one, and is retried every minute while the region stays
offline. **A check never moves back** on its own when the region recovers:
placements stay stable, which keeps the timing of every check stable too.

## Pinned placement

Choosing regions yourself (**Choose regions** in the check form, or an explicit
`regions` list in the API) pins the check: it runs from exactly those regions
and is never moved. If a pinned region goes offline, the check shows **No data**
and your organization's admins are told by email.

Private locations (`@my-location`) are always pinned: their credentials are
sealed to that location's agents and the target may only be reachable from
inside it.

## Switching

- **One check:** in the check form, **Choose regions** pins it and **Use
  automatic placement** makes it automatic. Over the API, `PATCH` with
  `"placement": "auto"` (it keeps its current regions and region count) or
  `"placement": "pinned"` (it keeps its current regions), or an explicit
  `regions` list to pin it somewhere else. `"regions": []` puts a check back on
  the default placement.
- **Every pinned check at once:** **Automatic placement** on the checks list
  (`POST /api/v1/orgs/{org}/checks/auto-placement`). Each check keeps its
  regions and its region count, so neither its cost nor where it runs today
  changes; it only gains failover. Checks on a private location are skipped.

Checks created before automatic placement existed are pinned, with one
exception: a check whose regions were exactly the platform's default regions
(nobody chose them) was switched to automatic placement with the same regions
and the same region count.

## Confirming across regions (`failQuorum`)

When a check runs from several regions, `failQuorum` says how many of them must
be failing, for the whole confirmation period, before the check is **down**
and an incident opens.

| `failQuorum` | Meaning |
|---|---|
| `default` | all regions for 1 or 2 regions, a majority for 3 or more |
| `all` | every region. Any passing region restarts the confirmation, as before |
| `majority` | more than half: 2 of 3, 3 of 4, 3 of 5 |
| a number | that many regions (capped at the check's region count) |

With 3 regions and the default (2 of 3):

- one region failing: the check shows **Regional issue** (a `warning`) with
  the failing region. No incident, no notification.
- two regions failing for the confirmation period: the check is **down** and
  an incident opens.
- recovery works the same way: the incident resolves once fewer than 2 regions
  have been failing for the recovery period. A region still failing then shows
  as a regional issue.

Only the check's current regions count. When a check is moved to another
region, what the old region last reported is ignored, and the new region counts
once it reports. A region silent for longer than the "No data" threshold
(3 periods, at least 5 minutes) does not count either.

Checks with 1 or 2 regions keep the exact behavior they always had. On the
public status page a regional issue shows as a warning, like any other.

## API and config-as-code

| Field | Meaning |
|---|---|
| `placement` | `pinned` or `auto` |
| `regionCount` | automatic only: how many regions run the check |
| `regionPool` | automatic only: candidate cloud regions; empty means any |
| `regions` | where the check runs now: your list when pinned, the current placement when automatic |
| `failQuorum` | `default`, `all`, `majority` or a number (a JSON string or number) |
| `effectiveFailQuorum` | read-only: what `failQuorum` resolves to for the current regions |

An automatically placed check exports as `placement: auto` with its
`regionCount` and `regionPool`, and **no** `regions`: those belong to the
scheduler, and re-importing them would pin the check. In a document,
`placement: auto` with no `regions` is meaningful: it switches an existing
pinned check to automatic placement. `placement: auto` together with a
`regions` list is refused (`INVALID_PLACEMENT`).
