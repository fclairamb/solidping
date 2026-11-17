---
model: sonnet
effort: low
---

# Chart dates follow the dashboard language

## Problem

dash0 formats dates with `date-fns` `format()` and never passes a `locale`, so
month and weekday names stay English whatever the UI language. Seen on the
French and German showcase stills: the response-time chart tooltip on the check
detail page reads "Nov 17, 20:48:32" under a French UI
(`web/dash0/src/components/checks/response-time-chart.tsx`, the tooltip
`format(new Date(data.ts), "MMM d, HH:mm:ss")` and the axis formatter
`"EEE HH:mm"` / `"MMM d"`). Nothing under `web/dash0/src` imports
`date-fns/locale`.

## Proposal

1. Add a small helper (e.g. `src/lib/date-locale.ts`) mapping
   `i18n.resolvedLanguage` to the `date-fns/locale` object (`enUS`, `fr`, `de`,
   `es`), and a `useDateFnsLocale()` hook.
2. Pass `{ locale }` to every `format()` that prints a month or weekday name,
   starting with the response-time chart. `grep -rn 'format(' web/dash0/src`
   to find the rest; pure numeric patterns (`HH:mm:ss`) need nothing.
3. Re-take the localized showcase stills afterwards
   (`make showcase-stills SHOWCASE_LOCALE=fr`, then `de`) and refresh them in
   the website repo's `static/img/showcase/<locale>/`.

## Tests

- Unit test on the helper: `fr` gives the French locale, an unknown language
  falls back to `enUS`.
- Unit or component test: the chart's tooltip formatter prints `nov.` for `fr`
  and `Nov` for `en`.
