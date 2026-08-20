---
model: sonnet
effort: medium
---

# Command palette is missing newer pages and only searches checks

## Problem

The ⌘K command palette ([CommandMenu.tsx](web/dash0/src/components/CommandMenu.tsx))
has drifted from the sidebar and only knows how to search one entity type.

**1. Missing static page entries.** The palette's `pages` list
(`CommandMenu.tsx:41-87`) predates several sidebar entries. Comparing with
[AppSidebar.tsx:60-125](web/dash0/src/components/layout/AppSidebar.tsx), the
palette is missing:

| Sidebar entry | Path | Icon |
|---|---|---|
| Status Updates (`statusUpdates`) | `/orgs/$org/status-updates` | `MessageSquare` |
| Maintenance Windows (`maintenanceWindows`) | `/orgs/$org/maintenance-windows` | `Wrench` |
| SLOs (`slos`) | `/orgs/$org/slos` | `Target` |
| My Pages (`myPages`) | `/orgs/$org/me/notifications` | `BellRing` |

The request also names "On call", but `onCall` → `/orgs/$org/on-call` is
already present (`CommandMenu.tsx:54`) — verify it renders/searches correctly
(the title comes from the `nav` i18n namespace) rather than adding a duplicate.
Maintenance Windows wasn't named in the request but is the only other
sidebar-vs-palette gap; add it in the same pass so the palette matches the
sidebar 1:1.

**2. Search only covers checks.** When the user types, the palette filters the
static page list client-side and queries checks via
`useChecks(org, { q, limit: 10 })` with a 300 ms debounce
(`CommandMenu.tsx:124-132`). No other entity is searched. The request: after
checks, also search **status pages, escalation policies, and SLOs** and show
them as their own result groups.

Backend note: only the checks list endpoint supports `q`
([handler.go:220](server/internal/handlers/checks/handler.go:220)). The other
three list hooks take no search param — `useStatusPages`
([hooks.ts:1886](web/dash0/src/api/hooks.ts:1886)), `useEscalationPolicies`
([hooks.ts:3577](web/dash0/src/api/hooks.ts:3577)), `useSlos`
([hooks.ts:5851](web/dash0/src/api/hooks.ts:5851)).

## Proposal

**Static pages** — extend the `pages` array in `CommandMenu.tsx` with the four
missing entries, in the same relative order as the sidebar, reusing the exact
sidebar icons and the existing `nav` i18n title keys (`statusUpdates`,
`maintenanceWindows`, `slos`, `myPages` already exist for the sidebar in all
four locales, `en`/`fr`/`de`/`es`).

**Entity search** — when the (debounced) search text is non-empty, render
additional result groups below the existing Checks group, one per entity:

- **Status pages** → navigate to `/orgs/$org/status-pages/$uid`
- **Escalation policies** → `/orgs/$org/escalation-policies/$uid`
- **SLOs** → `/orgs/$org/slos/$uid`

Since these endpoints have no `q` support and per-org cardinality is small
(SLOs are even entitlement-capped via `maxSlos`), fetch each full list with the
existing hooks and filter client-side on name/slug, capping each group at ~5
results. Only enable the queries while the palette is open (and ideally only
once the search text is non-empty) so opening ⌘K doesn't fan out requests.
Adding server-side `q` to the three endpoints is out of scope — client-side
filtering keeps this a frontend-only change.

Group order stays: Actions, Pages, Account, Organization, then Checks, Status
pages, Escalation policies, SLOs. Add the three new group-heading i18n keys
(e.g. `command.groupStatusPages`, `command.groupEscalationPolicies`,
`command.groupSlos`) to all four locale `nav.json` files.

**Tests** — extend [command-menu.spec.ts](web/dash0/e2e/command-menu.spec.ts):
navigating to one of the new static entries (e.g. type "slo" → SLOs page), and
an entity-search case (seed a status page / escalation policy / SLO, search its
name, assert the group renders and Enter navigates to its detail page).
