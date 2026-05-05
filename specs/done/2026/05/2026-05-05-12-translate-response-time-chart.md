# Translate the response-time chart and its pinned-result popover

**Status:** todo
**Owner:** frontend (dash0)
**Severity:** medium — the chart that dominates the check detail page is fully English; selecting a data point opens an info box that is also fully English.

## Problem

`web/dash0/src/components/checks/response-time-chart.tsx` and its child `web/dash0/src/components/checks/pinned-result-box.tsx` (rendered when a user clicks a dot on the chart) have **no `useTranslation` import**. Every label, button text, tooltip line, and aria-label is a hardcoded English literal. On the check detail page (e.g. `/dash0/orgs/default/checks/aa015625-07b1-44cf-a8e0-58ff44dc0545`) this is the largest visual block, so the language gap is glaring when the rest of the page renders in fr/de/es.

### `response-time-chart.tsx` — leaking strings

| Line | String | Context |
|---|---|---|
| 438 | `"Response Times"` | `<CardTitle>` |
| 446 | `"Full range"` | label of the toggle switch |
| 458 | `range` (raw `"hour"` / `"day"` / `"week"` / `"month"`, with `capitalize` CSS) | desktop button labels |
| 457 | `range[0].toUpperCase()` (mobile abbreviation) | mobile-only single-letter button |
| 467 | `"Showing all regions ({list})"` | shown above the chart when results span multiple regions |
| 474 | `"No data available"` | empty-state inside the chart container |
| 572 | `"No data"` | label rendered inside `<ReferenceArea>` for detected gaps |
| 555 | `"Click point for details"` | tooltip footer |
| 630–632 | `"Click again to open full page"` / `"Click for details"` | dot SVG `<title>` |

### `pinned-result-box.tsx` — leaking strings

| Line | String | Context |
|---|---|---|
| 96 | `aria-label="Close"` | dismiss button |
| 110 | `"Could not load details"` | fetch-error message |
| 116 | `"Duration"` | row label |
| 121 | `"Min / Max"` | row label |
| 129 | `"Availability"` | row label |
| 135 | `"Region"` | row label |
| 141 | `"Status"` | row label |
| 149 | `"Period"` | row label |
| 159 | `"Click again to open full page"` | footer hint |

## Scope

In scope:
- `web/dash0/src/components/checks/response-time-chart.tsx` — wire up `useTranslation` and replace the literals listed above.
- `web/dash0/src/components/checks/pinned-result-box.tsx` — same.
- `web/dash0/src/locales/{en,fr,de,es}/checks.json` — add `detail.chart` and `detail.resultBox` blocks.

Out of scope:
- `formatMs` / `adaptiveFormat` (lines 61–75 of the chart) — unit suffixes (`ms`, `s`) and `date-fns` `format()` patterns. These are universal abbreviations / format tokens, not translatable copy.
- The `(ongoing)` string used by `IncidentDuration` in the parent route file — already translated as `detail.ongoing`.
- Translating raw status codes (`up`, `down`, `created`, `running`) inside the chart — they are machine values shown on tooltips and are not translated anywhere else either.

## Translation keys

Add to `web/dash0/src/locales/en/checks.json` under `detail` (alongside the existing `summary` block from spec 11):

```json
"chart": {
  "title": "Response Times",
  "fullRange": "Full range",
  "rangeHour": "Hour",
  "rangeDay": "Day",
  "rangeWeek": "Week",
  "rangeMonth": "Month",
  "rangeHourShort": "H",
  "rangeDayShort": "D",
  "rangeWeekShort": "W",
  "rangeMonthShort": "M",
  "noDataAvailable": "No data available",
  "noData": "No data",
  "showingAllRegions": "Showing all regions ({{regions}})",
  "tooltipClickPoint": "Click point for details",
  "dotClickForDetails": "Click for details",
  "dotClickAgain": "Click again to open full page"
},
"resultBox": {
  "close": "Close",
  "couldNotLoad": "Could not load details",
  "duration": "Duration",
  "minMax": "Min / Max",
  "availability": "Availability",
  "region": "Region",
  "status": "Status",
  "period": "Period",
  "clickAgain": "Click again to open full page"
}
```

`fr/checks.json`:

```json
"chart": {
  "title": "Temps de réponse",
  "fullRange": "Plage complète",
  "rangeHour": "Heure",
  "rangeDay": "Jour",
  "rangeWeek": "Semaine",
  "rangeMonth": "Mois",
  "rangeHourShort": "H",
  "rangeDayShort": "J",
  "rangeWeekShort": "S",
  "rangeMonthShort": "M",
  "noDataAvailable": "Aucune donnée disponible",
  "noData": "Aucune donnée",
  "showingAllRegions": "Toutes les régions affichées ({{regions}})",
  "tooltipClickPoint": "Cliquer sur un point pour les détails",
  "dotClickForDetails": "Cliquer pour les détails",
  "dotClickAgain": "Cliquer à nouveau pour ouvrir la page complète"
},
"resultBox": {
  "close": "Fermer",
  "couldNotLoad": "Impossible de charger les détails",
  "duration": "Durée",
  "minMax": "Min / Max",
  "availability": "Disponibilité",
  "region": "Région",
  "status": "Statut",
  "period": "Période",
  "clickAgain": "Cliquer à nouveau pour ouvrir la page complète"
}
```

`de/checks.json`:

```json
"chart": {
  "title": "Antwortzeiten",
  "fullRange": "Vollständiger Bereich",
  "rangeHour": "Stunde",
  "rangeDay": "Tag",
  "rangeWeek": "Woche",
  "rangeMonth": "Monat",
  "rangeHourShort": "S",
  "rangeDayShort": "T",
  "rangeWeekShort": "W",
  "rangeMonthShort": "M",
  "noDataAvailable": "Keine Daten verfügbar",
  "noData": "Keine Daten",
  "showingAllRegions": "Alle Regionen werden angezeigt ({{regions}})",
  "tooltipClickPoint": "Punkt anklicken für Details",
  "dotClickForDetails": "Für Details anklicken",
  "dotClickAgain": "Erneut anklicken, um die vollständige Seite zu öffnen"
},
"resultBox": {
  "close": "Schließen",
  "couldNotLoad": "Details konnten nicht geladen werden",
  "duration": "Dauer",
  "minMax": "Min / Max",
  "availability": "Verfügbarkeit",
  "region": "Region",
  "status": "Status",
  "period": "Zeitraum",
  "clickAgain": "Erneut anklicken, um die vollständige Seite zu öffnen"
}
```

`es/checks.json`:

```json
"chart": {
  "title": "Tiempos de respuesta",
  "fullRange": "Rango completo",
  "rangeHour": "Hora",
  "rangeDay": "Día",
  "rangeWeek": "Semana",
  "rangeMonth": "Mes",
  "rangeHourShort": "H",
  "rangeDayShort": "D",
  "rangeWeekShort": "S",
  "rangeMonthShort": "M",
  "noDataAvailable": "No hay datos disponibles",
  "noData": "Sin datos",
  "showingAllRegions": "Mostrando todas las regiones ({{regions}})",
  "tooltipClickPoint": "Haga clic en un punto para ver detalles",
  "dotClickForDetails": "Haga clic para ver detalles",
  "dotClickAgain": "Haga clic de nuevo para abrir la página completa"
},
"resultBox": {
  "close": "Cerrar",
  "couldNotLoad": "No se pudieron cargar los detalles",
  "duration": "Duración",
  "minMax": "Mín / Máx",
  "availability": "Disponibilidad",
  "region": "Región",
  "status": "Estado",
  "period": "Período",
  "clickAgain": "Haga clic de nuevo para abrir la página completa"
}
```

Note on the short-range buttons: the existing code uses `range[0].toUpperCase()`, which is locale-dependent — picking the first letter of `hour`/`day`/`week`/`month` gives the right H/D/W/M for English but produces meaningless letters in other languages. Hence the explicit `rangeHourShort` / etc. keys above. German `Stunde`→`S` collides with `Semaine` in French (`S`), which is fine because each language gets its own short string; if the German team ever objects to `S` for *Stunde* (since `Sekunde` also starts with `S`) we can revisit, but it matches typical short-form conventions.

## Component changes

### `response-time-chart.tsx`

1. Add at top:
   ```ts
   import { useTranslation } from "react-i18next";
   ```
2. At the top of `ResponseTimeChart` function body:
   ```ts
   const { t } = useTranslation("checks");
   ```
3. Replace `<CardTitle>Response Times</CardTitle>` (line 438) with `<CardTitle>{t("detail.chart.title")}</CardTitle>`.
4. Replace `Full range` (line 446) with `{t("detail.chart.fullRange")}`.
5. Replace the inner spans of the range buttons (lines 457–458):
   ```tsx
   <span className="sm:hidden">{t(`detail.chart.range${range.charAt(0).toUpperCase() + range.slice(1)}Short`)}</span>
   <span className="hidden sm:inline">{t(`detail.chart.range${range.charAt(0).toUpperCase() + range.slice(1)}`)}</span>
   ```
   Drop the `capitalize` CSS class on the desktop span — translations are already capitalised correctly.
6. Replace `Showing all regions ({regions.join(", ")})` (line 467):
   ```tsx
   {t("detail.chart.showingAllRegions", { regions: regions.join(", ") })}
   ```
7. Replace `No data available` (line 474):
   ```tsx
   {t("detail.chart.noDataAvailable")}
   ```
8. Replace the `ReferenceArea` label `value: "No data"` (line 572):
   ```tsx
   value: t("detail.chart.noData"),
   ```
9. Replace tooltip `Click point for details` (line 555):
   ```tsx
   {t("detail.chart.tooltipClickPoint")}
   ```
10. Replace the dot `<title>` strings (lines 629–632):
    ```tsx
    {isSelected
      ? t("detail.chart.dotClickAgain")
      : t("detail.chart.dotClickForDetails")}
    ```

### `pinned-result-box.tsx`

1. Add at top:
   ```ts
   import { useTranslation } from "react-i18next";
   ```
2. Inside `PinnedResultBox` function body:
   ```ts
   const { t } = useTranslation("checks");
   ```
3. Replace each hardcoded literal with the corresponding `t("detail.resultBox.…")` call: `Close` (aria-label, line 96), `Could not load details` (line 110), `Duration` / `Min / Max` / `Availability` / `Region` / `Status` / `Period` row labels (lines 116, 121, 129, 135, 141, 149), and the footer `Click again to open full page` (line 159).

## Verification

1. `make dev-test` running on :4000 (CLAUDE.md baseline).
2. Visit the bug-report URL `http://localhost:4000/dash0/orgs/default/checks/aa015625-07b1-44cf-a8e0-58ff44dc0545` in each language:
   - English: chart title `Response Times`, range buttons `Hour Day Week Month` (mobile: `H D W M`), empty state `No data available`, tooltip `Click point for details`. Click any dot — pinned box shows `Duration` / `Min / Max` / `Availability` / `Region` / `Status` / `Period`.
   - French: `Temps de réponse`, `Heure Jour Semaine Mois` (mobile `H J S M`), `Aucune donnée disponible`, etc.
   - German: `Antwortzeiten`, `Stunde Tag Woche Monat`, `Keine Daten verfügbar`.
   - Spanish: `Tiempos de respuesta`, `Hora Día Semana Mes`, `No hay datos disponibles`.
3. Trigger the gap label by selecting `Full range` on a check whose history has gaps — the inline `No data` should now read `Aucune donnée` / `Keine Daten` / `Sin datos` in non-English.
4. Force the error state in the pinned box (open DevTools, throttle to Offline, click a dot) — message should be localised.

## Acceptance criteria

- [ ] `web/dash0/src/components/checks/response-time-chart.tsx` and `pinned-result-box.tsx` import `useTranslation` and contain zero English text in JSX/aria attributes.
- [ ] All four locale `checks.json` files contain matching `detail.chart` (15 keys) and `detail.resultBox` (9 keys) blocks. Verify with `jq '.detail.chart | keys | length, .detail.resultBox | keys | length' web/dash0/src/locales/*/checks.json`.
- [ ] `make build-dash0` and `make lint-dash` are green.
- [ ] Manual visual verification in all four languages on the bug-report URL.
