# Check form — replace Template button + expanded list with a sample picker

## Context

On the new-check form (`/dash0/orgs/$org/checks/new?checkType=dns`), choosing a sample config today takes three steps:

1. Pick a type from the type dropdown.
2. Click the **Template** button next to it.
3. Click one of the sample rows that appears stacked below the type/button row.

Source: `web/dash0/src/components/shared/check-form.tsx:318-325` (state) and `:1335-1374` (UI). Backed by `useSampleConfigs(checkType)` in `web/dash0/src/api/hooks.ts:1450-1462`, which is `enabled: false` and only fetches when the button is clicked.

Two real problems with the current UI:

- **Template is opaque.** A user landing on the form has no signal that ready-made configs exist. The button label "Template" is generic and easy to skip.
- **The sample list expands the page.** When clicked, three full-width rows push every field below them down. For types with more samples (HTTP has many) it is genuinely clunky.

User's proposal: when a type is selected, list the samples in a dropdown (combobox) and add a `[Load]` button next to it.

## Honest opinion

The user's instinct that the current UI is clunky is right. Two specific points where I'd push back, plus what I'd build instead:

**1. The separate `[Load]` button is one click too many.** The intended flow is "open dropdown → pick item → click Load". That is three actions for an operation the user already committed to by opening the picker. The natural Combobox pattern is "open → pick → apply (and close)" — two actions, no separate confirm. If the worry is "what if I want to peek at sample names without applying?", the popover already gives that — opening doesn't apply anything; closing without selecting doesn't apply anything; only clicking a row applies. A separate Load button buys nothing.

**2. "List samples directly" (i.e. flat dropdown visible inline) wastes room.** Embedding a `<select>` next to the Type field works for 1–4 samples but breaks when a type has 8+ samples (HTTP, looking at the existing sample registry). A popover-based picker scales the same way the existing Type picker already does.

**What I'd ship instead:** delete the Template button and the expanded list. Replace them with a single **"Load sample…"** popover button that mirrors the existing type-picker pattern (already in this same file — `PopoverTrigger` + scrollable button list). Click → popover opens → click a sample → form fills + popover closes. One click to discover, one click to apply.

If the user prefers their original dropdown + Load design after reading this, the implementation below is easy to swap — the data fetching and `applySample()` call don't change. The disagreement is purely the trigger UI.

## Scope

Frontend only. Single file: `web/dash0/src/components/shared/check-form.tsx`.

No backend change. The `/api/v1/check-types/samples?type=…` endpoint already returns what we need; `useSampleConfigs` already lazy-loads on demand.

Out of scope:
- Showing a sample preview (config diff) before applying. The current click-to-apply is destructive (it overwrites whatever the user already typed); that's a separate concern, and the existing UI has the same property.
- Eager-fetching samples on type change. Lazy-fetch on popover open is fine and avoids a request when the user knows what they want.
- Sample descriptions in the dropdown rows. Today only `sample.name` is shown; the API returns more fields, but adding them is a polish pass, not blocking.

## Approach (recommended — single picker)

### Remove
- The standalone `<Button>` labelled "Template" (`:1335-1352`).
- The `showSamples` state and its reset effect (`:320`, `:322-325`).
- The expanded list block (`:1356-1374`).

### Add
A second `Popover` next to the type picker, structured like the existing one already in this file:

```tsx
<Popover open={samplePickerOpen} onOpenChange={(open) => {
  setSamplePickerOpen(open);
  if (open && !fetchedSamples) {
    void fetchSamples();
  }
}}>
  <PopoverTrigger asChild>
    <Button
      type="button"
      variant="secondary"
      data-testid="check-load-template-button"
      disabled={!type}
    >
      {t("checks.loadSample", "Load sample…")}
    </Button>
  </PopoverTrigger>
  <PopoverContent className="w-[320px] p-1" align="end">
    {isFetchingSamples ? (
      <div className="flex items-center justify-center py-4">
        <Loader2 className="h-4 w-4 animate-spin" />
      </div>
    ) : !fetchedSamples || fetchedSamples.length === 0 ? (
      <div className="px-3 py-2 text-sm text-muted-foreground">
        {t("checks.noSamples", "No samples for this type")}
      </div>
    ) : (
      <div className="grid max-h-[280px] gap-0.5 overflow-y-auto">
        {fetchedSamples.map((sample) => (
          <button
            key={sample.slug}
            type="button"
            className="rounded-md px-3 py-2 text-left text-sm transition-colors hover:bg-accent"
            data-testid={`check-sample-${sample.slug}`}
            onClick={() => {
              applySample(sample);
              setSamplePickerOpen(false);
            }}
          >
            {sample.name}
          </button>
        ))}
      </div>
    )}
  </PopoverContent>
</Popover>
```

Replace the existing `showSamples` state with `samplePickerOpen`. Remove the type-change effect that reset `showSamples` and replace with a small effect that closes the popover on type change AND invalidates the cached samples query so the next open re-fetches:

```ts
const queryClient = useQueryClient();
useEffect(() => {
  setSamplePickerOpen(false);
  // The query key includes `type`, so React Query will refetch on next open
  // automatically. No explicit invalidate needed.
}, [type]);
```

### `applySample` is unchanged
Lines `:334-…` already do the right thing — keep them.

### i18n
Add two keys (`checks.loadSample`, `checks.noSamples`) to `web/dash0/src/locales/{en,fr,de,es}/checks.json`. The current "Template" string is hard-coded in JSX; this is also a chance to fix that.

| Key | en | fr | de | es |
|-----|----|----|----|-----|
| `checks.loadSample` | Load sample… | Charger un exemple… | Beispiel laden… | Cargar muestra… |
| `checks.noSamples` | No samples for this type | Aucun exemple pour ce type | Keine Beispiele für diesen Typ | No hay muestras para este tipo |

## Approach (fallback — if user wants their original design)

Same data layer, different trigger UI. Replace the Template button + expanded list with:

- A native `<select>` (or shadcn `<Select>`) seeded from `fetchedSamples`. On type change, eagerly call `fetchSamples()` so the dropdown is populated.
- A `[Load]` button next to it that calls `applySample(selectedSample)` only when clicked.
- Hide the row entirely when `fetchedSamples?.length === 0` after the eager fetch resolves.

Tradeoffs vs. recommended:
- Eager fetch on every type change → 1 extra API call per type the user tries.
- Two-control UI → wider, less compact.
- Pre-empts the "preview without applying" use case (which I argue we don't need).

Same files touched, same i18n keys (rename `loadSample` → `selectSample` if going this route).

## Verification

`make dev-test` is on port 4000.

1. Navigate to `/dash0/orgs/default/checks/new`.
2. Pick type **DNS**. The new "Load sample…" button is visible next to the type picker.
3. Click it. Popover opens; spinner briefly; three rows appear: Google DNS A Record, Cloudflare DNS A Record, GitHub DNS A Record.
4. Click "Cloudflare DNS A Record". Popover closes; form fields (name, slug, period, host, etc.) are populated for Cloudflare.
5. Change type to **HTTP**. Popover (if still open) closes. Click "Load sample…" again — fresh samples are fetched for HTTP.
6. Switch to a type that has no samples (if any exist; otherwise skip). Open popover — it shows "No samples for this type" instead of an empty list.
7. Switch language to FR/DE/ES via the language picker — the button label and empty-state message are translated.
8. Network tab: confirm `/api/v1/check-types/samples?type=…` is called only when the popover is opened (lazy), not on every type change.
9. `make build-dash0` passes (TypeScript).
10. `make lint-dash` passes.
11. Existing Playwright test that referenced `data-testid="check-load-template-button"` still finds the button (testid is preserved). Sample-row testid (`check-sample-${slug}`) is also preserved.

## Files touched

- `web/dash0/src/components/shared/check-form.tsx` — the bulk of the change.
- `web/dash0/src/locales/en/checks.json`
- `web/dash0/src/locales/fr/checks.json`
- `web/dash0/src/locales/de/checks.json`
- `web/dash0/src/locales/es/checks.json`

No backend change. No new API call. No new dependency.

---

## Implementation Plan

1. Add `useTranslation` import and `const { t } = useTranslation("checks")` to `check-form.tsx`.
2. Rename `showSamples` → `samplePickerOpen` and update the type-change reset effect to close the new popover.
3. Replace the standalone Template button + expanded sample list block with a single Popover that lazy-fetches on open and applies/closes on row click. Keep the existing `data-testid="check-load-template-button"` and `data-testid={"check-sample-${slug}"}` so any Playwright tests still match.
4. Add `loadSample` and `noSamples` keys (top-level under `checks`) to `web/dash0/src/locales/{en,fr,de,es}/checks.json`.
5. `make build-dash0` + `bun run lint` clean (no new errors versus main).
