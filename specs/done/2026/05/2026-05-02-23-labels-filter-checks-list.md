# Labels — filter UI on checks list

## Context

Once spec `2026-05-02-03` ships, users can attach labels to checks via the dashboard. But on the checks list page (`web/dash0/src/routes/orgs/$org/checks.index.tsx`), the only way to find checks by label is to scroll. The backend already supports `GET /api/v1/orgs/$org/checks?labels=k1:v1,k2:v2` (AND-logic filter, see `server/internal/handlers/checks/handler.go:85-96`), and `useInfiniteChecks()` already accepts a `labels?: string` parameter. What's missing is the UI to set it.

This spec adds a filter widget next to the existing search box that lets users add label filter chips with the same key/value autocomplete as the form picker.

## Honest opinion

**Reuse `<LabelInput>` from spec `03`, don't build a new component.** The visual primitive is identical: chips representing key:value pairs, with a key/value combobox to add new ones. The only difference is what `onChange` does — for the form, it updates form state; for the filter, it updates URL search params. Make `<LabelInput>` agnostic to that distinction (it already is — it just calls `onChange(next)`) and have this spec wire the filter side.

**Persist filter state in the URL, not local state.** Two reasons: (1) users share dashboards as links — `?labels=team:web,env:prod` should be a usable bookmark; (2) refreshing a filtered view shouldn't lose the filter. TanStack Router (already in use per `web/dash0/src/routes/orgs/$org/checks.index.tsx`) has `useSearch()` for typed URL state — use it.

**Don't add a "filter mode" toggle (AND vs OR).** Backend currently supports AND only. Pretending to offer OR by adding a UI toggle would either lie or require a backend change. Ship AND-only and revisit if users ask.

## Scope

**In:**
- Filter widget (uses `<LabelInput>`) on the checks list page.
- URL search-params persistence for label filters.
- Wire `labels` query string into `useInfiniteChecks()` call.
- "Clear filters" affordance.
- Playwright test for the filter flow.

**Out:**
- Backend changes (the filter endpoint exists and works).
- OR-logic filtering (separate spec if/when wanted).
- Saved filter views ("My team's checks" presets).
- Filter UI on other list pages (incidents, results, etc.) — separate spec if needed.

## URL contract

Search param: `labels=key1:value1,key2:value2` — same format as the existing API filter, so no client-side translation needed before sending.

Examples:
- `/dash0/orgs/default/checks` — no filter.
- `/dash0/orgs/default/checks?labels=env:prod` — single filter.
- `/dash0/orgs/default/checks?labels=env:prod,team:web` — AND of two filters.
- `/dash0/orgs/default/checks?q=api&labels=env:prod` — combine with search; both apply.

## UI placement

Existing filter bar lives at lines ~750-775 of `web/dash0/src/routes/orgs/$org/checks.index.tsx` (search input + buttons). Add the label filter immediately after the search input, on the same row when the viewport is wide enough; wrap below on narrow viewports.

```
┌─────────────────────────────────────────────────────────────┐
│ [search…           ]  Labels: [env: prod ×] [+ Add filter]  │
└─────────────────────────────────────────────────────────────┘
```

`[+ Add filter]` opens the same key/value combobox flow from `<LabelInput>` — when the user commits a chip, the URL updates and the list refetches.

If any filter chips are present, also show a small `Clear filters` text button that resets `labels` (and optionally `q`) to empty. Position: right of the chip strip.

## Implementation

### 1. URL state in route definition

Update the route's search-param schema (TanStack Router uses Zod or a custom validator — check the existing pattern in this file or sibling routes for `q`):

```ts
// inside the createFileRoute({...}) call for checks.index
validateSearch: (search: Record<string, unknown>) => ({
  q: typeof search.q === "string" ? search.q : "",
  labels: typeof search.labels === "string" ? search.labels : "",
}),
```

Read with `const { q, labels } = Route.useSearch()`.

### 2. Bridge URL string ↔ `Record<string, string>`

`<LabelInput>` works with `Record<string, string>`. The URL stores a comma-separated string. Two tiny helpers (put them next to the route file or in `web/dash0/src/lib/labels.ts` if a `lib/` exists):

```ts
export function parseLabelsParam(s: string): Record<string, string> {
  if (!s) return {};
  const out: Record<string, string> = {};
  for (const pair of s.split(",")) {
    const idx = pair.indexOf(":");
    if (idx <= 0) continue;
    const key = pair.slice(0, idx).trim();
    const value = pair.slice(idx + 1).trim();
    if (key && value) out[key] = value;
  }
  return out;
}

export function serializeLabelsParam(labels: Record<string, string>): string {
  return Object.entries(labels)
    .filter(([k, v]) => k && v)
    .map(([k, v]) => `${k}:${v}`)
    .join(",");
}
```

These are also useful in tests.

### 3. Render the filter widget

In `checks.index.tsx`, near the existing filter bar:

```tsx
const navigate = useNavigate({ from: Route.fullPath });
const labelFilters = parseLabelsParam(labels);

<div className="flex items-center gap-2">
  <LabelInput
    org={org}
    value={labelFilters}
    onChange={(next) => {
      void navigate({
        search: (prev) => ({ ...prev, labels: serializeLabelsParam(next) || undefined }),
        replace: true,
      });
    }}
    placeholder={{ key: t("checks.filterLabelKey", "Filter by key…"),
                   value: t("checks.filterLabelValue", "value…") }}
  />
  {(Object.keys(labelFilters).length > 0 || q) && (
    <button
      type="button"
      onClick={() => void navigate({ search: () => ({}), replace: true })}
      className="text-sm text-muted-foreground hover:text-foreground"
    >
      {t("checks.clearFilters", "Clear filters")}
    </button>
  )}
</div>
```

`replace: true` keeps the back button useful (typing filters doesn't pollute history with one entry per keystroke).

### 4. Wire into the data hook

Find the `useInfiniteChecks(...)` call (around line 335 of `checks.index.tsx`). Pass `labels` from the URL:

```ts
const { data, ... } = useInfiniteChecks({
  search: debouncedSearch,
  labels: labels || undefined,
  // ...rest of existing options
});
```

`useInfiniteChecks` already accepts `labels?: string` (verified in earlier exploration). If not, extend the hook in `web/dash0/src/api/hooks.ts` to forward it as a query string param.

### 5. Empty-state messaging

When the filtered list is empty, show a clear empty state distinct from the unfiltered "you have no checks yet" message:

```tsx
{checks.length === 0 && (Object.keys(labelFilters).length > 0 || q) ? (
  <EmptyState
    title={t("checks.noMatch", "No checks match your filters")}
    cta={
      <button onClick={() => navigate({ search: () => ({}) })}>
        {t("checks.clearFilters", "Clear filters")}
      </button>
    }
  />
) : checks.length === 0 ? (
  /* existing empty-state */
)}
```

## i18n

Add to `web/dash0/src/locales/{en,fr,de,es}/checks.json`:

| Key | en |
|-----|----|
| `checks.filterLabelKey` | Filter by key… |
| `checks.filterLabelValue` | value… |
| `checks.clearFilters` | Clear filters |
| `checks.noMatch` | No checks match your filters |

(FR/DE/ES translations during implementation — pattern matches existing files.)

## Tests

### Playwright (`web/dash0/tests/labels-filter-checks-list.spec.ts`)

Setup: log in, ensure at least 3 checks exist with varying labels (use the fixture pattern from existing tests; see `labels-on-check-form.spec.ts` from spec `03` for chip interaction helpers).

1. **Single filter narrows list.** Navigate to checks list. Type `env` in label key combobox → pick `environment` → type `prod` in value → pick `prod`. Verify URL becomes `…?labels=environment:prod`. Verify only checks with that label are visible.
2. **Two filters AND.** Add second chip `team: web`. Verify URL becomes `…?labels=environment:prod,team:web`. Verify only checks with BOTH labels remain.
3. **Remove filter chip.** Click `×` on `team: web` chip. URL drops to `…?labels=environment:prod`. List grows accordingly.
4. **Clear filters button.** Click `Clear filters`. URL has no `labels` param. Full list returns.
5. **Bookmark / refresh.** Navigate directly to `…?labels=environment:prod` (deep link). Page loads with filter applied and chip rendered.
6. **Combined with search.** Apply both `?q=api` and a label filter. Verify both narrow the list (intersection).
7. **Empty result.** Filter by a combination that matches no checks. Verify "No checks match your filters" empty state with Clear button.
8. **Autocomplete independence between fields.** Open the key combobox — suggestions are keys. Pick one. Open the value combobox — suggestions are values for that key only (different list).

## Verification

```bash
make dev   # backend at port 4000

# 1. Open the list page
open http://localhost:4000/dash0/orgs/default/checks

# 2. Walk the Playwright scenarios manually first

# 3. Build + lint + e2e
make build-dash0
make lint-dash
cd web/dash0 && bun test:e2e --grep "labels-filter"
```

## Files touched

- `web/dash0/src/routes/orgs/$org/checks.index.tsx` — filter widget, URL state, wire into hook, empty-state message.
- `web/dash0/src/lib/labels.ts` (new, or co-located with route file) — `parseLabelsParam` / `serializeLabelsParam` helpers.
- `web/dash0/src/api/hooks.ts` — verify `useInfiniteChecks` forwards `labels` (extend if it doesn't).
- `web/dash0/src/locales/{en,fr,de,es}/checks.json` — new keys.
- `web/dash0/tests/labels-filter-checks-list.spec.ts` — new Playwright spec.

No backend change. No new dependency. Reuses `<LabelInput>` from spec `03`.

## Implementation Plan

1. Add the URL search-param schema in the route's `validateSearch`.
2. Add `parseLabelsParam` / `serializeLabelsParam` helpers (with unit tests if a unit test setup exists for `lib/`).
3. Render `<LabelInput>` in the filter bar wired to `navigate({ search: ... })`.
4. Verify `useInfiniteChecks` accepts and forwards `labels` to the API; extend if needed.
5. Add empty-state branch for "no matches".
6. Add i18n keys in all 4 locales.
7. Write Playwright spec.
8. `make build-dash0` + `make lint-dash` clean.
9. `make dev` and walk the verification flow.
