---
model: sonnet
effort: high
---

# Move Discovery into the Organization section, and give it the route guard it never had

*Prompted by **Jens**, an early user, on 2026-09-16: "I do not know what
'Discovery' does in the left side menu." Placement decided by Florent — Discovery belongs on the
Organization page.*

## Why it moves

Discovery scans a network and turns findings into promotable check suggestions. It is:

- **admin-only in intent** — `POST /scans`, `/scans/:jobUid/cancel`, `/checks/promote`,
  `DELETE /checks` are all gated on `isAdmin(req)` in-handler
  ([`handlers/discovery/handler.go:26-33, 95, 223, 285, 325, 351`](server/internal/handlers/discovery/handler.go#L26-L33));
- **org-scoped and infrequent** — one scan per org at a time, run when you set the org up or add
  a network, not something you visit daily;
- **five sidebar rows' worth of nothing** for the ~everyone who is not an admin, and an
  unexplained word for an admin who has not read the docs.

That is the profile of an Organization tab, not a top-level nav item. It joins Members,
Invitations, Requests, Usage, Private locations, Uptime reports, Parameters, Audit and Settings —
the other admin-only, set-up-once surfaces.

## Current state

Routes under [`web/dash0/src/routes/orgs/$org/`](web/dash0/src/routes/orgs/$org/):

| File | Lines | What |
|---|---|---|
| `discovery.tsx` | 9 | pass-through `<Outlet/>`, **no guard** |
| `discovery.index.tsx` | 241 | scan list; source filter from `GET /discovery/types`; docs link `/docs/features/discovery` at `:158` |
| `discovery.new.tsx` | 449 | start-scan form; method in the URL (`?method=`, `validateSearch` `:45-49`); `lan \| container \| freebox \| kubernetes` |
| `discovery.$jobUid.tsx` | 5 | `<Outlet/>` |
| `discovery.$jobUid.index.tsx` | 562 | scan detail: chunk progress, cancel, grouped discovered checks, promote/dismiss |

Hooks: [`api/hooks.ts:5931-6166`](web/dash0/src/api/hooks.ts#L5931-L6166). Sidebar entry:
[`AppSidebar.tsx:224-243`](web/dash0/src/components/layout/AppSidebar.tsx#L224-L243), inside the
`user?.isAdmin` block.

### The bug this move happens to fix

`discovery.tsx` renders `<Outlet/>` **unconditionally**. Unlike
[`jobs.tsx:21-24`](web/dash0/src/routes/orgs/$org/jobs.tsx#L21-L24) and
[`organization.tsx`](web/dash0/src/routes/orgs/$org/organization.tsx), it has **no route-level
admin guard** — the sidebar merely hides the link. A non-admin who types
`/orgs/<org>/discovery` gets the page and can read every scan and every discovered host; only
the write actions 403.

There is a second, quieter asymmetry: the discovery route group is wired with
`api.NewGroup(...)` directly ([`server.go:1097-1104`](server/internal/app/server.go#L1097-L1104))
rather than through the `orgGroup` helper
([`server.go:796-801`](server/internal/app/server.go#L796-L801)), so it never picks up the
structural `RequireOrgWrite` viewer floor that every other org route gets. The in-handler
`isAdmin` checks cover the writes, so this is not currently exploitable — but it means discovery
is the one org surface whose authorization is entirely hand-rolled.

## Proposal

### A. Re-home the routes

`/orgs/$org/discovery/**` → **`/orgs/$org/organization/discovery/**`**:

| From | To |
|---|---|
| `discovery.index.tsx` | `organization.discovery.index.tsx` |
| `discovery.new.tsx` | `organization.discovery.new.tsx` |
| `discovery.$jobUid.tsx` | `organization.discovery.$jobUid.tsx` |
| `discovery.$jobUid.index.tsx` | `organization.discovery.$jobUid.index.tsx` |
| `discovery.tsx` | fold into the above — the `organization.tsx` layout already provides the guard |

Add a **Discovery** tab to the `tabs` array in
[`organization.tsx:22-46`](web/dash0/src/routes/orgs/$org/organization.tsx#L22-L46). That layout
already does `if (!user?.isAdmin) { navigate(... replace: true); return null; }` — which is
exactly the missing guard, acquired for free.

Placement in the tab row: after **Private locations**, before **Uptime reports**. Both are about
*where and how checks run*, so they read together.

That makes **ten** tabs. Verify [`TabNav`](web/dash0/src/components/shared/tab-nav.tsx) still
behaves on a 375px-wide viewport — every page must be usable on mobile. If it overflows badly,
fix `TabNav` (scrollable row), do **not** drop the tab.

### B. Redirect the old routes

`/orgs/$org/discovery`, `/orgs/$org/discovery/new` and `/orgs/$org/discovery/$jobUid` redirect to
their `organization/` equivalents, preserving search params — `discovery.new.tsx` keeps its scan
method in `?method=` ([`:45-49`](web/dash0/src/routes/orgs/$org/discovery.new.tsx#L45-L49)) and a
bookmarked "start a Kubernetes scan" link must survive.

### C. Backend

Optional but recommended in the same change: move the discovery group onto the `orgGroup` helper
so it inherits `RequireAuth + RequireOrgAccess + RequireOrgWrite` like every other org route, and
keep the in-handler `isAdmin` checks on top. If that turns out to change the response code for
any existing client, **skip it and say so** rather than papering over it — the frontend move is
the deliverable here.

Do not change the API paths. `/api/v1/orgs/:org/discovery/**` stays; `sp discovery …`
([`server/pkg/cli/discovery.go`](server/pkg/cli/discovery.go)) and the OpenAPI spec
([`openapi.yaml:4473-4780`](server/internal/app/openapi/openapi.yaml#L4473-L4780)) must keep
working untouched.

### D. Nav

The sidebar entry is removed by spec `07`, which owns
[`AppSidebar.tsx`](web/dash0/src/components/layout/AppSidebar.tsx) and
[`CommandMenu.tsx`](web/dash0/src/components/CommandMenu.tsx). **Do not edit those files here.**
Do add a Discovery entry to the command palette's `organization` group as part of spec `07`'s
work, not this one.

## Tests

- [`discovery.spec.ts`](web/dash0/e2e/discovery.spec.ts) (463 lines, 20+ cases) —
  `:56` and `:171` navigate via the sidebar and assert on the sidebar entry; repoint at the
  Organization tab. Note `:171`'s comment about "two 'Discovery' texts (sidebar + breadcrumb)"
  becomes wrong.
- [`discovery-promote.spec.ts`](web/dash0/e2e/discovery-promote.spec.ts),
  [`discovery-scan-method.spec.ts`](web/dash0/e2e/discovery-scan-method.spec.ts) — URL updates.
- [`docs-links.spec.ts:82-95`](web/dash0/e2e/docs-links.spec.ts#L82-L95) — navigates by sidebar
  link and asserts `docsHref === /docs/features/discovery`. Repoint; the docs link itself does
  not change.
- **New test, and the point of part A**: a non-admin member hitting
  `/orgs/$org/organization/discovery` directly is redirected to the org home. Nothing tests this
  today because there was no guard to test. Write it so it would have failed before the move.
- Redirect tests for the three legacy paths, including `?method=kubernetes` survival.

## Docs

[`web/docs/docs/features/discovery.md`](web/docs/docs/features/discovery.md) documents sources,
LAN/K8s options and the one-scan-per-org rule, but **never states that discovery is
admin-only** — which is part of why the nav entry read as mysterious. Add that, and update the
navigation instructions to "Organization → Discovery".
