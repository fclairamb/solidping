---
model: sonnet
effort: high
---

# The checks list shows every member of a group as a separate row, so one dead host reads as four unrelated failures

## Problem

When several checks probe the same host under different protocols (TCP + HTTP +
TLS + RDP), the checks index renders them all as individual rows. During a host
outage the list turns into a wall of red rows that are really one event, and in
the healthy steady state the list is 4× longer than the number of things being
monitored.

The grouping UI already exists — `CheckGroupSection` buckets checks by
`checkGroupUid` with a header showing the group name and `group.checkCount`
([checks.index.tsx:324](web/dash0/src/routes/orgs/$org/checks.index.tsx:324),
header around
[checks.index.tsx:365](web/dash0/src/routes/orgs/$org/checks.index.tsx:365)) —
but sections are always expanded and the header carries no health information,
so grouping today reduces nothing.

Prerequisite: spec `2026-08-01-01` adds `status` and `memberStatusCounts` to the
check-group API responses. This spec is the dashboard consumption of it.

## Proposal

Make group sections collapsible, with a status-bearing header that works as a
one-row summary when collapsed.

1. **Group header** gains:
   - the group's aggregate **status badge**, using the same status
     color/label mapping as check rows (statuses are the existing wire values:
     `up`, `down`, `degraded`, `warning`, `validating`, `created` — no new
     colors to invent, check the design reference first per the frontend rules);
   - a compact member summary from `memberStatusCounts`, e.g. "3/4 up" or
     "2 down · 2 up" — pick the simplest form that reads well on mobile;
   - a chevron toggle to collapse/expand the section.

2. **Collapse state**:
   - default: groups whose status is `up` may start collapsed, groups with any
     non-up status start expanded (the failure is what you came to see);
   - persist the user's manual toggles per org in `localStorage` (keyed by
     group uid); a manual choice wins over the default;
   - ungrouped checks ("No group" bucket) are never collapsed.

3. **Pagination interaction**: the list uses group-ordered pagination
   (spec `2026-07-20-05`, `specs/done/2026/07/`). Collapsing is purely visual —
   do **not** change the query, page size, or which rows are fetched. A
   collapsed group whose members span a page boundary simply hides the rows
   present on the current page; the header counts come from
   `memberStatusCounts` (server-side, whole group), so they stay correct
   regardless of pagination. Make sure the header renders even when the current
   page contains only a tail slice of the group's members.

4. **i18n**: new strings (summary format, collapse/expand tooltips) in all four
   locales (`en`, `fr`, `de`, `es` under `web/dash0/src/locales/`).

5. **Tests**: extend the checks-index Playwright coverage
   (`web/dash0/e2e/`): a group with one down member shows a `degraded` header
   and starts expanded; an all-up group collapses and its rows disappear while
   the "N/N up" summary remains; toggle state survives a reload; mobile
   viewport renders the header without overflow.

### Out of scope

- No changes to the group-ordered pagination backend.
- No "group by host" derivation — that's spec `2026-08-01-04`.
- No drag-to-reorder or nesting of groups.

## Implementation Plan

1. **API type**: add `status: string` and `memberStatusCounts?: Record<string, number>`
   to the hand-written `CheckGroup` interface in `web/dash0/src/api/hooks.ts`
   (mirrors the already-shipped OpenAPI schema — no backend/codegen change).

2. **Summary formatting helper** (in `checks.index.tsx`, colocated with
   `CheckGroupSection` since it's presentational and only used there): a
   `formatMemberSummary(counts, t)` function that:
   - returns `null` when there are no considered members;
   - returns the localized `"{{count}}/{{count}} up"` form when every counted
     member is up (this is exactly the collapse-eligible case);
   - otherwise joins non-zero buckets ordered by severity
     (`down → degraded → warning → validating → created → up`) as
     `"{{count}} {{label}}"` parts joined by `" · "`, e.g. `"1 down · 3 up"`.
   New i18n keys under `checks.json`'s `groupSummary` namespace (`allUp`,
   `part`, and one short label per wire status) in `en`, `fr`, `de`, `es`.

3. **Group header UI**: in `CheckGroupSection`,
   - add a `StatusBadge status={group.status}` next to the group name;
   - render the formatted summary as muted text next to the badge;
   - keep the header driven entirely by `group` (status/memberStatusCounts/
     checkCount all come from the groups list query, independent of which
     checks page has loaded) so it's correct even when the current page only
     has a tail slice of the group's members;
   - make the header row wrap (`flex-wrap`, `min-w-0`/truncate on the name) so
     the extra badge/summary don't overflow on narrow viewports;
   - wrap the chevron in a `Tooltip` with a localized expand/collapse label,
     add `aria-expanded`/keyboard toggling to the header row, and give the
     chevron and summary stable `data-testid`s for e2e.

4. **Collapse state**: replace the current plain `useState(false)` with:
   - a small localStorage helper (module-level, mirrors the existing
     try/catch pattern in `dashboard-page.tsx`/`last-auth-method.ts`) storing
     one JSON map per org at `solidping_collapsed_groups_${org}`:
     `{ [groupUid]: boolean }`;
   - `defaultCollapsed = group.status === "up"`;
   - `manualOverride` state seeded from the stored map (or `null` if absent);
   - rendered `collapsed = search ? false : (manualOverride ?? defaultCollapsed)`
     — searching still force-expands without persisting anything;
   - the toggle handler sets `manualOverride` and writes through to
     localStorage immediately.
   - `UngroupedChecksSection` is untouched — it already always renders its
     rows, satisfying "never collapsed" as-is.

5. **i18n**: add `menu.expandGroup` / `menu.collapseGroup` tooltip strings and
   the `groupSummary.*` keys to all four locale files.

6. **Design reference**: add a "Check group status header" example near the
   existing Status dot section in `design-reference.tsx` showing the badge +
   summary text combo, so it stays the canonical catalog entry per the
   frontend rules.

7. **E2E** (`web/dash0/e2e/`), new file `check-group-collapse.spec.ts`:
   - a group with one down (enabled) member shows a `Down`/degraded-flavored
     header and starts expanded (rows visible);
   - an all-up group's section starts collapsed (rows hidden) while the
     `"N/N up"` summary is still visible in the header;
   - clicking the chevron toggles collapse, and the choice survives a page
     reload (localStorage round-trip);
   - a mobile viewport (375px) renders the header without horizontal
     overflow.
   - Verify the pre-existing `checks-index-group-pagination.spec.ts` and
     `check-groups.spec.ts` still pass unmodified (their seeded groups have no
     completed runs, so `status` is `created` → expanded by default → no
     behavior change expected).
