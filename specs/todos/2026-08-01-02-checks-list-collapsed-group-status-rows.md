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
