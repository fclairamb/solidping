---
model: sonnet
effort: medium
---

# The incidents list can't be narrowed to a single check

## Problem

`/dash0/orgs/$org/incidents` only offers two filters: the state `Select`
(`all` / `active` / `acked` / `snoozed` / `resolved`) and the "show rolled up"
`Switch` — see [incidents.index.tsx:120-165](web/dash0/src/routes/orgs/$org/incidents.index.tsx#L120).
On an org with many checks (e.g. `stonal` on solidping.k8xp.com), the history
view `?state=all` is a long undifferentiated list and there is no way to focus
on the incidents of one particular check.

The gap is purely in the dashboard — every layer underneath already supports it:

- The API accepts a comma-separated `checkUid` filter:
  [handler.go:66](server/internal/handlers/incidents/handler.go#L66) →
  `opts.CheckUIDs`, and the service resolves each value as **uid or slug**
  ([service.go:2498](server/internal/handlers/incidents/service.go#L2498), issue #127).
- The `useIncidents` hook already takes `checkUid` and forwards it
  ([hooks.ts:1550](web/dash0/src/api/hooks.ts#L1550)); it is used that way by the
  check detail page, just never by the incidents list.
- A reusable, searchable, keyboard-navigable single-check selector already
  exists: [`CheckPicker`](web/dash0/src/components/shared/check-picker.tsx)
  (used by badges, SLO form, status page editor, dependencies).

So today the only way to get the same answer is to open the check's own detail
page, which shows a different, check-scoped incident list rather than the
filterable org-wide table.

## Proposal

Add a **check filter** to the incidents index page, URL-driven like the existing
filters.

1. **Search param.** Extend `validateSearch` in
   [incidents.index.tsx:38-54](web/dash0/src/routes/orgs/$org/incidents.index.tsx#L38)
   with `checkUid?: string` (undefined when absent, so a clean URL stays clean).
   `?checkUid=<uid>` and `?checkUid=<slug>` must both work end to end — the
   backend already resolves slugs, and a slug in the URL is what an operator
   would type by hand.

2. **UI.** Render a `CheckPicker` next to the state `Select` in the filter row,
   with a clear/"all checks" affordance (the picker already supports clearing to
   `undefined`). Follow
   [`design-reference.tsx`](web/dash0/src/routes/orgs/$org/design-reference.tsx)
   — reuse `CheckPicker` as-is, do not build a bespoke combobox. Keep the row
   responsive: it is `flex flex-wrap`, and the picker must not force a
   horizontal scroll on mobile. Give the trigger a `data-testid`
   (e.g. `incidents-check-filter`) via the existing `triggerTestId` prop.

3. **Wire the query.** Pass `checkUid: checkUid || undefined` to `useIncidents`.
   Nothing else changes — `with: "check"`, `hideSuppressed` and the live
   subscription already invalidate every filter variant of the query key.

4. **Don't drop the other filters on navigate.** Both existing `navigate()`
   calls rewrite `search` wholesale (`search: { state, showSuppressed }`), so
   adding a third param means every one of the three writers must carry the
   other two forward — otherwise picking a check silently resets `state=all`,
   or changing state clears the check. Prefer the functional form
   (`search: (prev) => ({ ...prev, ... })`) so a fourth filter can't reintroduce
   the bug.

5. **Cold deep-link.** `?checkUid=…` pasted into a fresh tab must apply on first
   render (the picker shows the check's name, the table is already filtered) —
   the picker resolves its own label from the uid via `useCheck`, and a *slug*
   value must resolve too. Verify this specifically; URL-seeded state on this
   layout route has regressed before.

6. **Empty state.** When a check filter is active and yields nothing, the
   "no incidents found" panel should say so in a way that points at the filter
   rather than implying the whole org is clean — extend the existing
   `stateFilter`-based message selection at
   [incidents.index.tsx:294-303](web/dash0/src/routes/orgs/$org/incidents.index.tsx#L294).

7. **i18n.** Add the new keys (filter placeholder, "all checks", filtered empty
   state) to **all four** locales — `en`, `fr`, `de`, `es` under
   `web/dash0/src/locales/*/incidents.json`. A missing key in a non-`en` locale
   is a failure, not a follow-up.

8. **Tests.** Extend `web/dash0/e2e/incidents.spec.ts`: pick a check from the
   dropdown → rows narrow to that check and the URL carries `checkUid`; combine
   with `state=all` and prove neither filter clears the other; reload the
   filtered URL cold and prove the filter is still applied and labelled.
   Include a **negative control** — an incident belonging to another check must
   be absent from the filtered table, not merely "the list got shorter".

Out of scope: multi-check selection (the API takes a comma-separated list, but
the UI stays single-select for now) and filtering by check group.
