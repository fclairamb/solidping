# Labels — input UI on check form

## Context

Backend support for labels on checks is complete — create/update/upsert all accept a `labels: Record<string, string>` field, and the list endpoint filters by `?labels=k:v,k2:v2`. But the dashboard has no way to set them. Users currently can attach labels only via the API or by importing a check JSON.

This spec adds a label picker section to `web/dash0/src/components/shared/check-form.tsx` so labels can be added/removed when creating or editing a check, with autocomplete on both key and value backed by the endpoint introduced in spec `2026-05-02-02`.

The component built here is reusable: spec `2026-05-02-04` will reuse the same `<LabelInput>` to drive list filtering, so it must be designed for both "form value" and "filter chip" usage.

## Honest opinion

Two design choices worth justifying upfront:

**1. Two-combobox-and-chip pattern over a single "k: v" text input.** Confirmed in planning — the structured pattern (key combobox + value combobox + committed chips) is more accessible, easier to test, and maps 1:1 to the underlying data shape. The single-text-input "type 'department: marketing'" pattern is slicker but introduces parsing edge cases (escaping `:` in values, paste behaviour, cursor handling) that don't pay off at this stage.

**2. Allow free-typing (creating new keys/values), don't gate on existing.** Autocomplete should suggest, not constrain. If a user wants `region: us-east-1` and that label doesn't exist yet, they should be able to type it and submit — `GetOrCreateLabel` on the backend handles the upsert. The UI should make existing values discoverable, not block new ones.

**Caveat to flag for the user:** the DB constraint on label keys is `^[a-z][a-z0-9-]{3,50}$` (`server/internal/db/postgres/migrations/001_initial.up.sql:266`). That means **minimum 4 characters total** for a key. The example `os: windows` from the original request would fail — `os` is 2 chars. We have two options:

- **(A) Loosen the regex** to `^[a-z][a-z0-9-]{1,50}$` (minimum 2 chars). Requires a migration. Easy, no breaking change.
- **(B) Keep the regex** and surface it clearly in the UI ("keys must be 4–51 lowercase letters/digits/hyphens, starting with a letter").

I'd recommend **(A)** — `os`, `db`, `ip` are all natural label keys and 4-char minimum is arbitrary. But this is a separate decision; this spec implements the UI assuming current constraints and surfaces the error nicely. **If you want (A), it's a 5-line migration in a separate spec; flag it in review.**

## Scope

**In:**
- New component `web/dash0/src/components/shared/label-input.tsx`.
- New TanStack Query hook `useLabelSuggestions` in `web/dash0/src/api/hooks.ts` calling the new endpoint.
- Integration of `<LabelInput>` into `check-form.tsx` (insertion point: after slug field, before CheckGroup picker).
- Wiring `labels` into create/update mutations.
- Display of attached labels (read-only chips) in the check row on the checks list page — light touch only, full filter UI is in spec `04`.
- i18n keys for the new strings.
- Playwright test.

**Out:**
- Filter UI on the checks list (spec `04`).
- Bulk label operations (assign label X to N checks at once).
- Visual color coding per label key.
- Label management page (rename a key everywhere, delete unused labels).

## Component design

### File: `web/dash0/src/components/shared/label-input.tsx`

```tsx
type LabelInputProps = {
  org: string;                                    // for the suggestions API call
  value: Record<string, string>;
  onChange: (next: Record<string, string>) => void;
  disabled?: boolean;
  placeholder?: { key?: string; value?: string }; // i18n-friendly overrides
};

export function LabelInput(props: LabelInputProps) { ... }
```

### Visual layout

```
┌──────────────────────────────────────────────────┐
│ Labels                                            │
│                                                   │
│ [department: marketing ×]  [team: growth ×]      │
│                                                   │
│ ┌─────────────┐   ┌─────────────┐                │
│ │ key       ▼ │ : │ value     ▼ │  [ Add ]       │
│ └─────────────┘   └─────────────┘                │
└──────────────────────────────────────────────────┘
```

- Chip strip is the committed labels. Each chip uses `Badge` (`web/dash0/src/components/ui/badge.tsx`) with an `×` button to remove (calls `onChange({...value, [key]: undefined})` — actually delete the key, don't set to empty string).
- Below the chips is an "Add label" row: two combobox inputs side-by-side with `:` between them, then an `Add` button (or `Enter` from the value field commits). Pressing Enter in the key field auto-focuses the value field.
- "Add" button is disabled until both key and value are non-empty AND key passes the regex. Add commits the chip and clears the two inputs.
- Editing an existing chip: click it → chip becomes inline-editable as the same key/value combobox pair, with the Add button labeled "Save". Cancel reverts.

### Combobox internals

Use `cmdk` (already a dependency — see `web/dash0/src/components/CommandMenu.tsx` for the canonical usage). Each combobox is a `<Popover>` containing a `<Command>` with `<CommandInput>` (for typing) and `<CommandList>` populated from `useLabelSuggestions`.

- **Free-typing allowed:** if the user types a value not in the suggestions, show a "Use '<typed>'" item at the top (cmdk supports this via `<CommandItem>` rendered conditionally based on whether the input matches any existing suggestion). Selecting it commits the typed value.
- **Suggestion sort:** preserve the API's order (count DESC, value ASC). Don't re-sort on the client.
- **Debounce:** 200ms via `use-debounce` if it's already a dependency, otherwise a tiny inline `useEffect` with `setTimeout`/`clearTimeout`. Don't add a new dep just for this.

The autocomplete endpoint is defined in spec `2026-05-02-02`. Don't merge this spec until that one is deployed; the form will work without it (free typing still allowed) but suggestions will be empty.

### Validation

- Key regex: `^[a-z][a-z0-9-]{3,50}$` — match the DB constraint at `server/internal/db/postgres/migrations/001_initial.up.sql:266`. Show error inline below the key combobox: "Use 4–51 lowercase letters, digits, or hyphens, starting with a letter."
- Value: max 200 chars (`server/internal/db/postgres/migrations/001_initial.up.sql:267`). Show character counter when nearing limit.
- Duplicate key: if the user tries to add a key that's already in `value`, the Add button should be disabled with an inline note ("This key is already set — edit the existing chip"). Don't silently overwrite.

### `useLabelSuggestions` hook

In `web/dash0/src/api/hooks.ts`:

```ts
export function useLabelSuggestions(
  org: string,
  opts: { key?: string; q?: string; limit?: number; enabled?: boolean }
) {
  const params = new URLSearchParams();
  if (opts.key) params.set("key", opts.key);
  if (opts.q) params.set("q", opts.q);
  if (opts.limit) params.set("limit", String(opts.limit));

  return useQuery({
    queryKey: ["labels", org, opts.key ?? "", opts.q ?? "", opts.limit ?? 50],
    queryFn: () => apiFetch<{ data: { value: string; count: number }[] }>(
      `/api/v1/orgs/${org}/labels?${params}`
    ),
    enabled: opts.enabled ?? true,
    staleTime: 30_000, // labels don't churn fast; cache for 30s to reduce traffic on every keystroke
  });
}
```

Use `apiFetch` from `web/dash0/src/api/client.ts` (per the existing pattern). Cache key includes `key` and `q` so each combobox's state is isolated.

## Integration into check form

### File: `web/dash0/src/components/shared/check-form.tsx`

1. **Add to local state** (near the other check fields, around line ~270-330 where state is declared):
   ```ts
   const [labels, setLabels] = useState<Record<string, string>>(initial?.labels ?? {});
   ```
   Make sure to thread `initial?.labels` through wherever the form is reset on `initial` change.

2. **Insert the section** after the slug field (around line 1453, before the CheckGroup `<Select>`):
   ```tsx
   <FormSection title={t("checks.labels", "Labels")}>
     <LabelInput
       org={org}
       value={labels}
       onChange={setLabels}
     />
   </FormSection>
   ```
   Use the same section/label/wrapper styling as adjacent fields — match what slug/group are wrapped in.

3. **Wire into mutations.** Find where `useCreateCheck` / `useUpdateCheck` mutations are called from `check-form.tsx` (likely in the submit handler near the bottom of the form). Add `labels` to the payload. Verify `useCreateCheck` / `useUpdateCheck` types in `web/dash0/src/api/hooks.ts` already accept `labels: Record<string, string>` — backend already does, so the type may already be there per the earlier exploration. Extend if missing.

4. **PATCH semantics:** when calling `useUpdateCheck`, the backend distinguishes nil (no change) vs empty map (clear all). Ensure that:
   - If the form's `labels` state is unchanged from `initial.labels`, omit the field (or send `undefined`) so it doesn't trigger an unnecessary update.
   - If the user removes all chips, send `{}` (empty object) — backend will clear them.

   Cleanest implementation: track whether the labels state has been touched (`dirty` flag) and only include `labels` in the mutation payload when dirty.

## Display labels on the checks list (read-only)

### File: `web/dash0/src/routes/orgs/$org/checks.index.tsx`

Add a chip strip to each check row showing up to 3 attached labels, with a `+N` overflow count if more exist. Keep it small — small `Badge` with `variant="secondary"` next to the check name or in a dedicated column. This makes labels visible in the list before the filter UI ships in spec `04`.

The list endpoint already returns `labels` per check (verified in `handlers/checks/service.go` — `CheckResponse` has `Labels map[string]string`). No new API call.

If this proves visually noisy in review, demote to a tooltip or push to spec `04`. Worth trying inline first.

## i18n

Add to `web/dash0/src/locales/{en,fr,de,es}/checks.json`:

| Key | en | fr | de | es |
|-----|----|----|----|-----|
| `checks.labels` | Labels | Étiquettes | Labels | Etiquetas |
| `checks.labelKeyPlaceholder` | key | clé | Schlüssel | clave |
| `checks.labelValuePlaceholder` | value | valeur | Wert | valor |
| `checks.labelAdd` | Add | Ajouter | Hinzufügen | Añadir |
| `checks.labelRemove` | Remove label | Supprimer l'étiquette | Label entfernen | Eliminar etiqueta |
| `checks.labelKeyInvalid` | Use 4–51 lowercase letters, digits, or hyphens, starting with a letter. | … | … | … |
| `checks.labelKeyDuplicate` | This key is already set — edit the existing label. | … | … | … |
| `checks.labelUseTyped` | Use "{{value}}" | Utiliser « {{value}} » | „{{value}}" verwenden | Usar "{{value}}" |
| `checks.labelNoSuggestions` | No matches | Aucune correspondance | Keine Treffer | Sin coincidencias |

(Fill the `…` cells during implementation; pattern matches existing files.)

## Tests

### Playwright (`web/dash0/tests/`, follow the pattern of existing `*.spec.ts`)

Test file: `labels-on-check-form.spec.ts`.

1. **Create check with labels:** log in → New check → fill name/slug/type → click Add label, type `department` in key combobox → suggestions empty (first time) → continue typing, click "Use 'department'" → focus moves to value → type `marketing` → press Enter → chip `department: marketing` appears. Add `team: growth` similarly. Save. Reload edit page → both chips are visible.
2. **Autocomplete on second check:** Create a second check, click Add label, type `dep` → `department` appears in suggestions (from prior check). Pick it. Type `mar` in value → `marketing` appears. Pick it. Save.
3. **Edit and remove:** Open the first check's edit page → click `×` on `team: growth` → save → reload → only `department: marketing` remains.
4. **Validation:** type `OS` in key combobox → red error message about lowercase requirement. Add button disabled.
5. **Duplicate key blocked:** add `department: x` → save. Re-edit → try to add another `department: y` chip → Add button disabled with the duplicate message. (Editing the existing chip is the right path.)

### Unit tests (optional but encouraged)

If the codebase has component-level tests (Vitest, etc. — verify in `web/dash0/`), test `<LabelInput>` in isolation:
- value/onChange round-trip
- Add disabled until both fields valid
- Removing a chip updates value correctly

## Verification

```bash
# 1. Backend has the autocomplete endpoint from spec 02 deployed
make dev   # port 4000

# 2. Open dash0
open http://localhost:4000/dash0/orgs/default/checks/new

# 3. Manually walk the Playwright scenarios above

# 4. Build + lint
make build-dash0
make lint-dash

# 5. Run Playwright
cd web/dash0 && bun test:e2e --grep "labels-on-check-form"
```

## Files touched

- `web/dash0/src/components/shared/label-input.tsx` — new component.
- `web/dash0/src/components/shared/check-form.tsx` — add Labels section, thread `labels` into state and mutation payloads.
- `web/dash0/src/api/hooks.ts` — add `useLabelSuggestions`; verify `labels` field on Create/Update types and add if missing.
- `web/dash0/src/routes/orgs/$org/checks.index.tsx` — display chips per row (small touch).
- `web/dash0/src/locales/{en,fr,de,es}/checks.json` — i18n keys.
- `web/dash0/tests/labels-on-check-form.spec.ts` — new Playwright spec.

No new npm dependency. `cmdk` and `@radix-ui/react-popover` are already installed; `Badge` already exists.

## Implementation Plan

1. Implement `useLabelSuggestions` in `api/hooks.ts`. Hand-test against backend spec 02's curl examples.
2. Build `<LabelInput>` standalone — render in a sandbox route or Storybook-style stub if available; otherwise iterate against the form directly.
3. Verify validation rules (regex, dup, value length) with manual cases.
4. Wire into `check-form.tsx`: state, section, mutation payload (with dirty-tracking for PATCH).
5. Add chip strip to the checks list rows.
6. Add i18n keys across all 4 locales.
7. Write Playwright spec.
8. `make build-dash0` + `make lint-dash` clean.
9. `make dev`, run through the verification checklist manually.
