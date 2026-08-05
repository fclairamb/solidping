---
model: sonnet
effort: medium
---

# Search inputs should sync to the URL as `?q=$query`

## Problem

Every dash0 list page keeps its text-search box in local `useState` only, so the
query is invisible to the router: it is lost on reload or back-navigation, and a
filtered view can't be shared or bookmarked as a link. Affected pages:

- [checks.index.tsx:893](web/dash0/src/routes/orgs/$org/checks.index.tsx:893) —
  `const [search, setSearch] = useState("")`, debounced 300 ms and sent to the
  API as `q` (line 960). Its `validateSearch` (line 154) already puts `labels`,
  `status`, and `groupBy` in the URL — the text query is the one filter missing.
- [status-updates.index.tsx:171](web/dash0/src/routes/orgs/$org/status-updates.index.tsx:171)
- [status-pages.index.tsx:136](web/dash0/src/routes/orgs/$org/status-pages.index.tsx:136)
- [maintenance-windows.index.tsx:135](web/dash0/src/routes/orgs/$org/maintenance-windows.index.tsx:135)
- [integrations.index.tsx:63](web/dash0/src/routes/orgs/$org/integrations.index.tsx:63)
- [escalation-policies.index.tsx:121](web/dash0/src/routes/orgs/$org/escalation-policies.index.tsx:121)
  (state at line 60)
- [dependencies.index.tsx:31](web/dash0/src/routes/orgs/$org/dependencies.index.tsx:31)
  (named `filter` there)

The REST API already uses `q` as the search parameter (repo `CLAUDE.md`, REST
API conventions), so `?q=` is the natural URL spelling on the frontend too.

## Proposal

On each affected list route, reflect the search query in the URL as `?q=$query`:

1. Add `q` to the route's `validateSearch` (create one where the route has
   none), typed `string | undefined`, empty string normalized to `undefined` so
   an empty box produces a clean URL. Follow the existing shape in
   [checks.index.tsx:154](web/dash0/src/routes/orgs/$org/checks.index.tsx:154).
2. Keep a local input state for responsive typing, **seeded from the URL `q` on
   mount**, and write the debounced value back to the URL with
   `navigate({ search: (prev) => ({ ...prev, q: value || undefined }), replace: true })`.
   `replace: true` so each keystroke doesn't pollute browser history; preserve
   the route's other search params via the functional `search` updater.
   - Plain `validateSearch` alone is NOT enough for the input: cold deep-links
     under a layout route drop URL-only state — seed local state from the URL on
     mount and write through (known dash0 pitfall; see the pattern already
     handled for `labels`/`status` in checks.index.tsx around line 883).
3. Behavior is otherwise unchanged: checks keeps sending `q` to the API; the
   pages that filter client-side keep doing so, just fed from the synced value.
4. Rename `dependencies.index.tsx`'s `filter` state to match the common
   pattern while at it (URL param is still `q`).

Acceptance:

- Typing in any list search box updates the address bar to `?q=…` (debounced,
  no history spam); clearing it removes the param.
- Deep-linking `/orgs/$org/checks?q=api` (cold load, not client-side nav) shows
  the input pre-filled with `api` and the list filtered.
- Existing params (`labels`, `status`, `groupBy`, `tab`, …) survive q updates
  and vice versa.
- A Playwright E2E covers at least the checks page: type → URL updates → reload
  → input and filter persist.
