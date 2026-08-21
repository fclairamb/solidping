---
model: sonnet
effort: medium
---

# The Organization breadcrumb has no leaf for Requests, Private locations or Uptime reports

## Problem

`Breadcrumbs` in [web/dash0/src/routes/orgs/$org.tsx:463-493](web/dash0/src/routes/orgs/$org.tsx#L463)
resolves the Organization section's leaf label from a hardcoded four-way chain:

```ts
const isMembers      = routeIds.has("/orgs/$org/organization/members");
const isInvitations  = routeIds.has("/orgs/$org/organization/invitations");
const isSettings     = routeIds.has("/orgs/$org/organization/settings");
const isUsage        = routeIds.has("/orgs/$org/organization/usage");
const subLabel = isMembers ? t("members")
  : isInvitations ? t("invitations")
  : isUsage ? t("usage")
  : isSettings ? t("settings")
  : null;
```

The section has grown past those four. When `subLabel` is `null` the component
falls through to the `else` branch and renders **only** a non-link
"Organization" crumb — so the breadcrumb silently stops one level short and
gives no clue which tab you are on:

| URL | Current breadcrumb | Expected |
|---|---|---|
| `/orgs/default/organization/requests` | `Organization` | `Organization › Requests` |
| `/orgs/default/organization/private-locations` | `Organization` | `Organization › Private locations` |
| `/orgs/default/organization/report-schedules` | `Organization` | `Organization › Uptime reports` |

All three are first-class tabs in the section's `TabNav`
([organization.tsx:23-40](web/dash0/src/routes/orgs/$org/organization.tsx#L23)),
so the breadcrumb is the only navigation surface that pretends they don't exist.

Three further routes in the same section are affected by the same gap and should
be fixed in the same pass rather than left as a second round of this bug:

- `/orgs/$org/organization/ai` ([organization.ai.tsx](web/dash0/src/routes/orgs/$org/organization.ai.tsx)) — not a tab, but reachable by URL; `nav:ai` ("AI assistants") already exists.
- `/orgs/$org/organization/private-locations/register` ([organization.private-locations.register.tsx](web/dash0/src/routes/orgs/$org/organization.private-locations.register.tsx))
- `/orgs/$org/organization/report-schedules/new` and `/$uid` ([new](web/dash0/src/routes/orgs/$org/organization.report-schedules.new.tsx), [$uid](web/dash0/src/routes/orgs/$org/organization.report-schedules.$uid.tsx))

Secondary defect: the `privateLocations` i18n key **does not exist** in any
locale file. `organization.tsx:33` only works because it passes an inline
English default (`t("nav:privateLocations", "Private locations")`), so the tab
label is untranslated in fr/de/es today. `reportSchedules` and `requests` do
exist in all four locales.

## Proposal

Extend the Organization branch of `Breadcrumbs` so every route under
`/orgs/$org/organization` resolves a leaf, and so the routes that nest one level
deeper render the full three-crumb trail.

### 1. Replace the ternary chain with a route-id → label table

A flat lookup keyed by route id, so adding a tab later can't silently regress
the breadcrumb again:

```ts
const ORG_SECTION_LABELS: Record<string, string> = {
  "/orgs/$org/organization/members": "members",
  "/orgs/$org/organization/invitations": "invitations",
  "/orgs/$org/organization/requests": "requests",
  "/orgs/$org/organization/usage": "usage",
  "/orgs/$org/organization/private-locations": "privateLocations",
  "/orgs/$org/organization/report-schedules": "reportSchedules",
  "/orgs/$org/organization/ai": "ai",
  "/orgs/$org/organization/settings": "settings",
};
```

Match with `matches.some((m) => m.routeId.startsWith(id))` (not exact
`routeIds.has`) so the layout routes — `organization.private-locations.tsx` and
`organization.report-schedules.tsx` are `<Outlet/>` wrappers with `.index`,
`.new`, `.$uid` children — still resolve their section label on child routes.
Keep the "Organization" crumb a plain `<span>` on `organization.index` and a
`<Link>` to `/orgs/$org/organization/members` whenever a leaf is present, which
is the existing behaviour.

### 2. Deep leaves for the sub-routes

Where a section has children, the middle crumb becomes a `<Link>` to the
section's list and the leaf names the record — the same shape the SLOs branch
already uses ([$org.tsx:530-590](web/dash0/src/routes/orgs/$org.tsx#L530)):

- `report-schedules/new` → `Organization › Uptime reports › New`
- `report-schedules/$uid` → `Organization › Uptime reports › <schedule name>`,
  fetched with `useReportSchedule(org, isReportScheduleDetail ? (params.uid ?? "") : "")`.
  **Gate on the section flag** — `uid` is a shared param name across on-call,
  escalation-policies and SLOs, and the existing hooks in this component all
  short-circuit on the wrong section for exactly that reason
  ([$org.tsx:166-186](web/dash0/src/routes/orgs/$org.tsx#L166)). Fall back to
  `uid.slice(0, 8)` while the name is loading, as the other detail crumbs do.
- `private-locations/register` → `Organization › Private locations › Register`

Reuse the existing `t("form.new")` / register labels rather than inventing new
i18n keys where an equivalent already exists.

### 3. Add the missing `privateLocations` key

Add `"privateLocations"` to `nav.json` in **all four** locales (en/fr/de/es),
then drop the inline default at
[organization.tsx:33](web/dash0/src/routes/orgs/$org/organization.tsx#L33) so
the tab and the breadcrumb read from one source. Suggested values:

| Locale | Value |
|---|---|
| en | `Private locations` |
| fr | `Emplacements privés` |
| de | `Private Standorte` |
| es | `Ubicaciones privadas` |

### 4. Test

Add a breadcrumb test modelled on
[slos.spec.ts:358](web/dash0/e2e/slos.spec.ts#L358), covering all three reported
URLs plus the two deep report-schedule routes. Include the negative control that
test already demonstrates: on a section index the section crumb is plain text
(`getByRole("link", …)` has count 0), and on a child route it *is* a link — so
"the leaf is visible" can't pass vacuously on a blank page.
