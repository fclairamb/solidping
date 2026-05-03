# Status0 — locale-aware date and time formatting

## Context

The public status page at `web/status0` lets the visitor pick a language via the flag dropdown (or `?lang=fr`). Translatable strings switch correctly via `react-i18next`, but **dates and times rendered by the availability bar and response-time chart are formatted with `toLocaleDateString(undefined, …)` / `toLocaleTimeString(undefined, …)`**. Passing `undefined` as the first argument tells the browser to use its OS locale, which has nothing to do with the active i18n language.

Concretely, in the screenshot from the user (lang=fr, French UI), the chart x-axis still shows `Apr 1`, `May 2` instead of `1 avr.`, `2 mai`. Same problem affects:

- The availability-bar tooltip header (`Apr 1`, `May 2` instead of `1 avr.`, `2 mai`).
- The response-time chart x-axis ticks.
- The response-time chart tooltip header (date + time).

A user whose browser is set to French but who picks `?lang=de` would see German strings with French dates, and vice versa. The user-facing language selector should be the single source of truth.

## Scope

Only the date and time formatting calls in two component files:

- `web/status0/src/components/shared/availability-bar.tsx` — 1 call
- `web/status0/src/components/shared/response-time-chart.tsx` — 4 calls (2 in `formatTick`, 2 in `CustomTooltip`)

A sibling spec (`2026-05-02-09-status0-translate-availability-bar-and-chart.md`) adds the missing translation keys and `t()` calls in the same files. The two specs touch overlapping lines; whoever lands second should rebase trivially.

Out of scope:
- The number formatting `toFixed(2)` / `toFixed(3)` for percentages. French uses `,` as the decimal separator and would render `100,000 %`; German/Spanish similar. This is a real but separate concern — doing it correctly means switching to `Intl.NumberFormat(i18n.language, { … })` and possibly tweaking the surrounding layout. Defer to a later spec; today's status pages from competitors (Statuspage, Hund, BetterStack) don't all localize numbers either, so it's not a clear regression.
- Time-zone handling. Dates are already rendered in the visitor's local zone (the default for `toLocale*String`), which is the correct behaviour for a status page.

## Approach

Replace `undefined` with `i18n.language` from `useTranslation()`. The hook is already being added to both files by the sibling spec; if that spec landed first, the `t` and `i18n` are already in scope, just use them. If this spec lands first, the hook needs to be added — see the detailed edits below for both starting states.

### `web/status0/src/components/shared/availability-bar.tsx`

`formatDate` is currently a top-level function (line 21). Two options:

**Preferred — inline the format inside `AvailabilityBar` so it can close over `i18n.language`.** Delete the standalone `formatDate` function. Inside `AvailabilityBar` after `const { t } = useTranslation();` (added by sibling spec — if that spec hasn't landed yet, also add `useTranslation` import and call), add:

```ts
const { t, i18n } = useTranslation();

const formatDate = (dateStr: string) => {
  const date = new Date(dateStr + "T00:00:00");
  return date.toLocaleDateString(i18n.language, {
    month: "short",
    day: "numeric",
  });
};
```

The tooltip JSX (`{formatDate(point.date)}`) doesn't change.

### `web/status0/src/components/shared/response-time-chart.tsx`

Two functions need locale awareness: `formatTick` (top-level helper used by recharts' `tickFormatter`) and `CustomTooltip` (a component, so a hook is OK there).

**`formatTick`** is called from inside the recharts `<XAxis tickFormatter={(v) => formatTick(v, spansDays)} />`. It can't be a hook itself, but the parent `ResponseTimeChart` *can* read `i18n.language` and pass it in:

```ts
function formatTick(isoStr: string, spansDays: boolean, locale: string) {
  const date = new Date(isoStr);
  if (spansDays) {
    return date.toLocaleDateString(locale, {
      month: "short",
      day: "numeric",
    });
  }
  return date.toLocaleTimeString(locale, {
    hour: "numeric",
    minute: "2-digit",
  });
}
```

In `ResponseTimeChart`:

```ts
const { i18n } = useTranslation();   // sibling spec already adds this — if not yet, also add the import
// …
<XAxis
  dataKey="time"
  tickFormatter={(v) => formatTick(v, spansDays, i18n.language)}
  …
/>
```

**`CustomTooltip`** already calls `useTranslation()` after the sibling spec (for `t("noData")`). If this spec lands first, add it. Then:

```tsx
const { t, i18n } = useTranslation();
…
<p className="font-medium">
  {date.toLocaleDateString(i18n.language, {
    month: "short",
    day: "numeric",
  })}{" "}
  {date.toLocaleTimeString(i18n.language, {
    hour: "numeric",
    minute: "2-digit",
  })}
</p>
```

## A subtle gotcha — `i18n.language` value format

`i18n.language` returns whatever value the language detector picked, typically the short code: `"en"`, `"fr"`, `"de"`, `"es"`. These are valid BCP-47 tags and `Intl.DateTimeFormat` accepts them — `(new Date()).toLocaleDateString("fr", { month: "short", day: "numeric" })` returns `2 mai` as expected.

If the detector ever produces a longer tag like `"fr-CA"` (it can, depending on browser settings), the formatting still works correctly — the BCP-47 fallback chain handles it. No normalization needed.

## Verification

1. `make build-status0` — confirms TypeScript still compiles after the signature change to `formatTick`.
2. `make dev-test` already running on port 4000. Open in browser and visually compare:
   - `http://localhost:4000/status0/default/test?lang=en` — chart x-axis shows `Apr 1`, `May 2`; tooltip shows `Apr 1` + `12:00 AM` (or whatever).
   - `?lang=fr` — x-axis shows `1 avr.`, `2 mai`; tooltip shows `1 avr.` + `00:00`.
   - `?lang=de` — x-axis shows `1. Apr.`, `2. Mai`; tooltip uses 24h format `00:00`.
   - `?lang=es` — x-axis shows `1 abr`, `2 may`.
3. Switch languages via the flag dropdown without reloading — the chart should re-render with the new locale (recharts re-renders when its props change; `tickFormatter` closes over `i18n.language` which causes a new function reference each render, which is fine for this volume of data).
4. Hover over availability-bar day cells to confirm the tooltip date follows the active language.

## Final grep check

After the change, this should return zero matches in `web/status0/src/components/`:

```bash
rtk grep -rn 'toLocaleDateString(undefined\|toLocaleTimeString(undefined\|toLocaleString(undefined' web/status0/src/components/
```

---

## Implementation Plan

1. In `availability-bar.tsx`, move the standalone `formatDate` helper into `AvailabilityBar`'s body (or a closure at render time) so it can close over `i18n.language` and pass that as the first argument to `toLocaleDateString`.
2. In `response-time-chart.tsx`, change `formatTick` to accept a `locale` param and have the parent `ResponseTimeChart` pass `i18n.language`; in `CustomTooltip`, read `i18n` via `useTranslation()` and pass it to both `toLocaleDateString` and `toLocaleTimeString`.
3. `make build-status0 lint-dash` and verify no remaining `toLocale*String(undefined,` calls in `web/status0/src/components/`.
