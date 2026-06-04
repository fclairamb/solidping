# Redesign the checks-list label filter (faceted picker)

## Problem

On `/dash0/orgs/$org/checks`, the **Labels** filter in the toolbar is the
`LabelInput` component (`web/dash0/src/components/shared/label-input.tsx`,
used at `checks.index.tsx:831`). It renders, permanently expanded in the
toolbar row:

```
Labels:  [ filter by key… ]  :  [ value… ]  [ Add ]
```

This is a **data-entry form** wedged into a **filter toolbar**, and it looks
and behaves wrong for the job:

1. **It's a form, not a filter.** Two fixed-width (`w-40`, 160px) text boxes,
   a floating `:` glyph between them, and an explicit **Add** button. Filtering
   should feel instant — "Add" reads like you're authoring a record, not
   narrowing a list. The mental model collides with the separate "Clear
   filters" button.
2. **The orphaned `:` separator** (`<span className="px-1 pt-2">`) is a
   disconnected piece of punctuation. The two boxes don't read as one unit.
3. **Always-on footprint.** Even with no filter applied, the whole
   key/colon/value/Add apparatus occupies the toolbar, competing for
   horizontal space with the search box and the status `Select`.
4. **Inconsistent with our own toolbar language.** The status filter right
   next to it is a single compact `Select` trigger (`w-[140px]`). The label
   filter should likewise collapse to one small trigger that opens a focused
   picker.
5. **Poor on mobile.** Under `flex-wrap`, the row collapses into key box /
   orphaned colon / value box / Add button stacked vertically — exactly what
   CLAUDE.md's "all pages must be fully usable on mobile" rule warns against.

The same `LabelInput` is also used in the check **form** (`check-form.tsx:2121`)
to *author* a check's labels. There the explicit two-box + **Add** model is
defensible (you are building key:value records). **This spec only changes the
filter usage on the checks list** — see Non-goals.

## Goal

Replace the toolbar label filter with a single compact control that follows
the well-known faceted-filter pattern (Linear / GitHub labels / shadcn
data-table faceted filter):

- Applied filters render as removable chips (we already have this).
- A single small **`+ Label`** trigger button opens **one** popover.
- Inside the popover, a guided two-step `cmdk` list: pick a **key**, then pick
  (or type) a **value**. Selecting a value **applies the filter immediately** —
  no separate "Add" button.
- The URL contract (`?labels=key:value,…` via `serializeLabelsParam`) is
  unchanged, so deep links and sharing keep working.

### Non-goals

- **Do not** change `LabelInput` in the check form (`check-form.tsx`). The
  authoring flow keeps its current two-box + Add behaviour and all its test
  IDs (`label-key-input`, `label-value-input`, `label-key-use-typed`,
  `label-key-error`, `label-key-duplicate`, `label-chips`,
  `label-chip-remove-*`). `check-labels.spec.ts` must stay green untouched.
- No backend changes. The `/api/v1/orgs/{org}/labels` suggestions endpoint and
  `useLabelSuggestions` already return keys (no `key` param) and per-key values
  (`key=…`) with counts — reuse as-is.

## Proposed design

Collapsed state in the toolbar:

```
Labels:  ‹env: prod ✕›  ‹team: ops ✕›   [ + Label ]   Clear
```

Popover, step 1 — choose key (counts on the right):

```
┌────────────────────────────┐
│ Filter by label…           │   ← cmdk input
├────────────────────────────┤
│ env                     12 │
│ team                     8 │
│ service                  5 │
│ Use "regi…"  (typed key)   │   ← only when typed value is a valid new key
└────────────────────────────┘
```

Popover, step 2 — choose value for the chosen key (`‹ env` is a back action):

```
┌────────────────────────────┐
│ ‹ env :  value…            │
├────────────────────────────┤
│ prod                     9 │
│ staging                  2 │
│ dev                      1 │
│ Use "canary"               │
└────────────────────────────┘
```

Selecting a value calls `onChange({ ...value, [key]: val })`, the chip appears,
and the popover resets to step 1 (so you can stack another filter) or closes —
implementer's call; resetting to step 1 is preferred so adding several filters
is fast. Picking an already-filtered key shows an inline "already filtering by
this key" hint and does not advance.

### Implementation

1. **New component** `web/dash0/src/components/shared/label-filter.tsx`
   exporting `LabelFilter` with the same external contract as the filter usage
   today:
   ```ts
   type LabelFilterProps = {
     org: string;
     value: Record<string, string>;
     onChange: (next: Record<string, string>) => void;
   };
   ```
   - Render the applied chips (lift the existing chip markup from
     `LabelInput`: `Badge variant="secondary"` + `X` remove button, keep
     `data-testid="label-chips"` and `label-chip-remove-{key}`).
   - Render a `+ Label` trigger: `Button variant="outline" size="sm"` with a
     `Tags` (or `Plus`) lucide icon, `data-testid="label-filter-trigger"`.
   - Build the picker on `Popover` + `cmdk` `Command` (both already deps and
     used in `label-input.tsx` — reuse the `SuggestionCombobox` data plumbing:
     `useLabelSuggestions`, `useDebounced`, `KEY_REGEX`, `VALUE_MAX`,
     `SUGGESTION_DEBOUNCE_MS`, `SUGGESTION_LIMIT`). Consider extracting the
     shared `useDebounced` + constants into a small module so both
     `label-input.tsx` and `label-filter.tsx` import them rather than
     duplicating.
   - Internal step state: `"key" | "value"` plus the chosen key. Back action
     returns to `"key"`.
   - Validation: typed key must pass `KEY_REGEX`; reuse the existing error copy
     ("Use 3–51 lowercase letters, digits, or hyphens, starting with a
     letter."). Typed value must be 1–`VALUE_MAX` chars.
   - Test IDs: `label-filter-key-input`, `label-filter-value-input`,
     `label-filter-key-use-typed`, `label-filter-value-use-typed`,
     `label-filter-back`.

2. **Wire it into the toolbar** at `checks.index.tsx:828-859`: swap
   `<LabelInput …>` for `<LabelFilter org={org} value={labelFilters}
   onChange={…} />`, keeping the exact same `onChange` body
   (`serializeLabelsParam` → `navigate({ search })`) and the existing
   "Clear filters" button (`data-testid="clear-label-filters"`). Drop the
   `placeholder` prop and the `min-w-[200px]` wrapper that only existed to give
   the two boxes room — the trigger is compact now.

3. **Design reference.** Add `LabelFilter` to
   `web/dash0/src/routes/orgs/$org/design-reference.tsx` with its import line,
   per the repo convention that the reference stays canonical.

### Alternative considered (lighter touch, not recommended)

Keep two inline boxes but visually merge them into one segmented input (shared
border, inline `:`), drop the **Add** button, and apply on Enter/value-select.
This removes the orphaned colon and the Add button but keeps the always-on
toolbar footprint and the two-dropdown focus dance. The faceted popover above
is preferred because it also reclaims toolbar space and matches the status
`Select` next to it.

## Verification

Automated:
- `make lint` and dash type-check (`bun run build`) clean.
- `check-labels.spec.ts` passes **unchanged** (proves the form authoring path
  was not touched).
- Add filter coverage to the checks E2E (extend `checks.spec.ts` or a new
  `check-label-filter.spec.ts`):
  - Open `+ Label`, pick a key, pick a value → chip appears and the URL gains
    `?labels=key:value`.
  - Add a second key:value → URL has both, comma-separated.
  - Remove a chip via `label-chip-remove-{key}` → URL updates.
  - "Clear filters" empties `?labels` and removes all chips.
  - Deep-link directly to `?labels=env:prod` → chip renders on load and the
    list is filtered.

Manual:
- Toolbar reads cleanly with 0, 1, and several active filters.
- Mobile width (≤375px): trigger + chips wrap gracefully; popover is usable and
  scrolls; no orphaned punctuation.
- Keyboard only: open popover, type, arrow + Enter to pick key then value,
  Esc closes. Light and dark mode both look right.

## Notes

- Honest scope check: the filter is the ugly part the screenshot shows; the
  form's authoring control is acceptable as-is, so it stays out of scope to
  keep this change small and low-risk. A separate follow-up could polish the
  form control (merge the boxes, lose the floating colon) if desired.
