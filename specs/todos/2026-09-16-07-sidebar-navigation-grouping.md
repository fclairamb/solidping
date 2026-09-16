---
model: opus
effort: high
---

# Group the sidebar, surface Organization, and rename "My pages"

*Reported by **Jens**, an early user, on 2026-09-16, replying to a "what do you
think of SolidPing?" email. He set up real checks before writing, so this is first-run feedback
from someone who got through onboarding and then had to find things.*

This spec owns **every navigation file**: [`AppSidebar.tsx`](web/dash0/src/components/layout/AppSidebar.tsx),
[`CommandMenu.tsx`](web/dash0/src/components/CommandMenu.tsx), and `web/dash0/src/locales/*/nav.json`.
The sibling specs (`08` badges, `09` discovery, `10` dependencies) own their own route files and
deliberately do **not** touch these three, to keep a batch run from conflicting on them. If those
specs land in the same batch, this one must be applied **last**; if they do not land, the nav
entries this spec removes must be removed anyway and the routes left reachable by URL.

## What Jens said

> There were many concepts and some of them sound similar, like "status page" and "status
> updates" but are two different things which confused me. And the left side menu took a while to
> get comfortable with. **You have many features in the left side menu and they are not grouped or
> anything.**

> - Finding the "Organization" page took me a while
> - I do not know what "My pages" does in the left side menu

He then proposed his own grouping ("Current status" / "Checks setup" / "Overall technical
setup"). The names aren't right and his buckets overlap — he put "Status page" in two of them —
but the instinct is correct and worth honouring: a first-time user wants *what's happening now*
separated from *what I configure* separated from *account-level plumbing*.

## Current state

[`AppSidebar.tsx:52-131`](web/dash0/src/components/layout/AppSidebar.tsx#L52-L131) is a flat
`navItems` array of **14 entries** rendered as one undifferentiated `SidebarMenu`
([lines 191-213](web/dash0/src/components/layout/AppSidebar.tsx#L191-L213)), followed by a second
ungrouped block for Discovery + Jobs behind `user?.isAdmin`
([lines 215-259](web/dash0/src/components/layout/AppSidebar.tsx#L215-L259)), then a test-mode
block. No `SidebarGroupLabel` is used anywhere.

**Organization has exactly one visible entry point in the entire app**: a `DropdownMenuItem`
inside the avatar menu at the bottom-left corner
([`AppSidebar.tsx:339`](web/dash0/src/components/layout/AppSidebar.tsx#L339)) — confirmed by
`grep -rn 'to="/orgs/\$org/organization"' web/dash0/src/` returning that single hit. The only
other route to it is the Cmd-K command palette
([`CommandMenu.tsx:89-95`](web/dash0/src/components/CommandMenu.tsx#L89-L95)), which Jens plainly
never found. So "it took me a while" is the expected outcome, not bad luck.

### "My pages" is a pun that only misfires in English

`myPages` points at `/orgs/$org/me/notifications`, whose page
([`me.notifications.tsx:46-60`](web/dash0/src/routes/orgs/$org/me.notifications.tsx#L46-L60))
lists **incident notifications you personally received** — time / incident / channel, with a
delivery status badge. "Pages" means *pagings*, as in being paged.

Every other locale already translates the intent and drops the pun:

| Locale | String | File |
|---|---|---|
| en | **"My pages"** | [`en/nav.json:17`](web/dash0/src/locales/en/nav.json#L17) |
| fr | "Mes alertes" | [`fr/nav.json:17`](web/dash0/src/locales/fr/nav.json#L17) |
| es | "Mis alertas" | [`es/nav.json:17`](web/dash0/src/locales/es/nav.json#L17) |
| de | "Meine Benachrichtigungen" | [`de/nav.json:17`](web/dash0/src/locales/de/nav.json#L17) |

English is the outlier, and it sits **three rows below "Status Pages"** in the same menu. There
is no reading of "My pages" in that context other than "status pages that belong to me". The
route path, the icon (`BellRing`), the component name, the API hook and the page's own
description all say *notifications*. `common.json → myNotifications.title` is also `"My pages"`
and must be fixed with it.

## Proposal

### A. Group the sidebar into four labelled sections

Use the existing `SidebarGroupLabel` primitive
([`sidebar.tsx:364-384`](web/dash0/src/components/ui/sidebar.tsx#L364-L384)) — it already handles
the icon-collapsed sidebar (`group-data-[collapsible=icon]:-mt-8 … :opacity-0`), so **no new
primitive is needed** and the collapsed rail keeps working unchanged.

| Group | Items | Gate |
|---|---|---|
| **Monitoring** | Dashboard, Checks, Incidents, Events, SLOs | all members |
| **Alerting** | Integrations, On-call, Escalation policies, **My alerts** | all members |
| **Public status** | Status pages, Status updates, Maintenance | all members |
| **Administration** | Organization | `isAdmin` |
| | Jobs | `isSuperAdmin` — see spec-level note below |

Removed from the sidebar entirely: **Badges** (spec `08` moves it under the check),
**Dependencies** (spec `10` deletes the page), **Discovery** (spec `09` moves it into
Organization).

Net effect for a regular member: **14 flat items → 12 items in three labelled groups of
5 / 4 / 3**. For an org admin, one extra group with one item.

Notes for the implementer:

- SLOs sit in Monitoring rather than in a group of their own. It is the weakest placement in this
  table; if it feels wrong once rendered, moving it is fine — do not add a fifth one-item group.
- Keep `navItems` a data structure, not JSX: refactor it to
  `navGroups: { labelKey, gate?, items: [...] }[]` and render with a nested map, so the next
  change is a data edit. Preserve the existing `isActive` logic
  ([lines 196-199](web/dash0/src/components/layout/AppSidebar.tsx#L196-L199)) and `tooltip` prop
  (the tooltip is what the collapsed rail shows, and group labels vanish there).
- Group labels must be translated keys in `nav.json`, not literals.

### B. Put Organization in the sidebar

Add `Organization` (`Building` icon, `/orgs/$org/organization`) as the Administration group's
first item, rendered only when `user?.isAdmin`.

**Keep the avatar-dropdown entry as well.** It is where people look for account-adjacent things
and removing it would break a path existing users already know. Two entry points to an
admin-only page is not clutter — one was the bug.

### C. Rename `myPages`

- `en/nav.json` → `"myAlerts": "My alerts"` (matches fr/es intent; "alerts" beats
  "notifications" because it is shorter in the rail and the page is specifically about being
  paged for an incident).
- Rename the **key** from `myPages` to `myAlerts` in all four locales, and update the two call
  sites ([`AppSidebar.tsx:121-125`](web/dash0/src/components/layout/AppSidebar.tsx#L121-L125),
  [`CommandMenu.tsx:71`](web/dash0/src/components/CommandMenu.tsx#L71)) plus the breadcrumb at
  [`routes/orgs/$org.tsx:1001-1007`](web/dash0/src/routes/orgs/$org.tsx#L1001-L1007).
- Fix `common.json → myNotifications.title` in all four locales the same way.
- Leave the **route path** `/orgs/$org/me/notifications` alone. Nothing user-visible depends on
  it and a redirect is not worth the churn.
- `CommandMenu.tsx:71` currently files this entry under `group: "pages"` — the palette's
  *section* named "pages". That compounds the ambiguity; it is fine to leave the palette grouping
  as-is, but do not let the renamed item read "My alerts" under a heading that says "Pages" if
  that looks wrong in practice.

### D. Jobs → super-admin only

Change the Jobs sidebar gate from `isAdmin` to `isSuperAdmin`
([`AppSidebar.tsx:244-256`](web/dash0/src/components/layout/AppSidebar.tsx#L244-L256)) **and** the
route guard in [`jobs.tsx:21-24`](web/dash0/src/routes/orgs/$org/jobs.tsx#L21-L24) — including
the `// JobsLayout guards the admin-only Jobs section` comment above it
([`jobs.tsx:9-11`](web/dash0/src/routes/orgs/$org/jobs.tsx#L9-L11)), which will otherwise say
"admin" while the code says super admin.

Definitions, so this is done against the right bit
([`AuthContext.tsx:30-35`, `:314`](web/dash0/src/contexts/AuthContext.tsx#L30-L35)):
`isAdmin = role ∈ {owner, admin, superadmin}` (a per-org membership role);
`isSuperAdmin = role === "superadmin"` (a server-wide `users.super_admin` column,
[`models/auth.go:63`](server/internal/db/models/auth.go#L63)).

**Leave the backend gates alone.** The org-scoped observability endpoints are
`RequireOrgAdmin` ([`server.go:1021-1038`](server/internal/app/server.go#L1021-L1038)) and the
`/system/*` twins are `RequireSuperAdmin`. Jobs exposes queue depth and check-schedule state for
the caller's own org — an org admin reading that is not a privilege problem, and tightening the
API would break `sp jobs` / `sp check-jobs` for self-hosters who are org admins but not super
admins. This change is about **sidebar clutter for a first-time user**, which is exactly what
Jens reported. Say so in the commit message so a future reader does not "finish the job" by
tightening the API.

The super-admin-only cross-org scope toggle
([`jobs.index.tsx:204-224`](web/dash0/src/routes/orgs/$org/jobs.index.tsx#L204-L224)) becomes
unconditionally visible once the page itself is super-admin-only. Simplify it or leave it; do not
leave a dead `isSuperAdmin` check that now always passes without a comment.

### E. Command palette parity

[`CommandMenu.tsx:49-110`](web/dash0/src/components/CommandMenu.tsx#L49-L110) mirrors the sidebar.
Apply the same removals (`dependencies` at `:61`, `badges` at `:70`) and the `myPages` rename.
Consider grouping the palette's `pages` group to match the new sidebar sections — optional, and
only if it does not fight the palette's existing `GroupKey` shape.

## Tests

- `bun run test:unit` — [`locale-parity.test.ts`](web/dash0/src/locales/locale-parity.test.ts)
  compares **key sets** across `en/fr/de/es`. Renaming `myPages` → `myAlerts` in one locale and
  not the others fails it, and so does adding group-label keys to `en` only. This suite is **not
  part of the usual backend QA loop** — run it explicitly.
- E2E updates required (all in `web/dash0/e2e/`):
  - [`docs-links.spec.ts:88`](web/dash0/e2e/docs-links.spec.ts#L88) clicks the sidebar
    **Discovery** link — repoint at the Organization tab (spec `09`).
  - [`docs-links.spec.ts:114`](web/dash0/e2e/docs-links.spec.ts#L114) clicks sidebar
    **Dependencies** — delete with spec `10`.
  - [`docs-links.spec.ts:191`](web/dash0/e2e/docs-links.spec.ts#L191) clicks sidebar **Jobs** as
    the "page with no docs mapping" case. The E2E user `test@test.com` **is** a super admin
    ([`testdata.go:221`](server/test/testdata/testdata.go#L221)), so this keeps working — but the
    comment above it about org admins is now wrong and must be corrected.
  - [`jobs.spec.ts:17`](web/dash0/e2e/jobs.spec.ts#L17) "Jobs sidebar link is visible for admin"
    → rename and re-scope to super admin. [`jobs.spec.ts:109`](web/dash0/e2e/jobs.spec.ts#L109)
    already covers the non-admin redirect; **add a case for an org admin who is not a super
    admin** — that is the new behaviour and nothing tests it today.
  - [`badges.spec.ts`](web/dash0/e2e/badges.spec.ts) navigates via the sidebar — spec `08`.
- **New E2E**: assert the four group labels render, that Organization is in the sidebar for an
  admin and absent for a non-admin, and that the rail in collapsed mode still shows every item
  (group labels hidden) — the collapsed case is the one the `SidebarGroupLabel` CSS could break.
- Run the **full** dash0 E2E suite as the gate, not just the touched files: nav is shared by
  every spec, and per-file runs have hidden cross-suite breakage here before.

## Reply to Jens

Tell him the sidebar is being grouped, roughly along the lines he sketched, and that "My pages"
was a bad pun on *paging* — it is his own alert history, and it is being renamed. Worth adding
that Cmd-K opens a search over every page and check, since he was hunting for Organization by
eye.
