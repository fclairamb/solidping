# Fix: badges width/minWidth input locked

## Problem

On the badges configuration page (`/dash0/orgs/:org/badges`), the **Width**
and **Min Width** number inputs are effectively read-only. Typing a new value
does nothing — the field snaps back to its current value on every keystroke.

**Root cause** (`web/dash0/src/routes/orgs/$org/badges.tsx`):

Both inputs are fully controlled by URL search state (`value={width}`).
`onChange` fires `updateSearch` on every keystroke, which triggers a TanStack
Router navigation. `validateSearch` rejects any value outside `[60, 800]`
for width and `[1, 800]` for minWidth, so intermediate values typed while
editing (e.g. the transient "30" when clearing "300" to type "500") fail
validation and the param is dropped → the input reverts to the default.

Additionally, `Number(e.target.value) || undefined` converts an empty field to
`undefined`, causing an immediate snap-back before the user finishes typing.

## Fix

Buffer width/minWidth in local component state; commit to the URL only on
`onBlur` (or when Enter is pressed).

```tsx
// local state mirrors the in-progress typed value
const [localWidth, setLocalWidth] = useState(String(width));

// keep in sync when URL changes (e.g. browser back/forward)
useEffect(() => setLocalWidth(String(width)), [width]);

<Input
  type="number"
  min={60} max={800} step={10}
  value={localWidth}
  onChange={(e) => setLocalWidth(e.target.value)}
  onBlur={() => {
    const n = Number(localWidth);
    if (!isNaN(n) && n >= 60 && n <= 800) updateSearch({ width: n });
    else setLocalWidth(String(width)); // revert to last valid
  }}
  onKeyDown={(e) => e.key === "Enter" && e.currentTarget.blur()}
/>
```

Apply the same pattern to `minWidth`.

## Scope

- `web/dash0/src/routes/orgs/$org/badges.tsx` only — no backend changes needed.
- Add/update Playwright test in `web/dash0/e2e/` to assert that typing a new
  width and tabbing away updates the badge URL.

## Acceptance criteria

1. User can clear the width field and type a new value (e.g. 500); the badge
   URL updates after blur/Enter.
2. Typing an out-of-range value and blurring reverts the input to the last
   valid value without crashing.
3. Browser back/forward keeps the input in sync with the URL.
