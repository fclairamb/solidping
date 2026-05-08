# Translate the check-detail availability table

**Status:** todo
**Owner:** frontend (dash0)
**Severity:** medium — the at-a-glance "1d / 7d / 30d / 1y" availability table on every check detail page is fully English.

## Problem

`web/dash0/src/components/checks/availability-table.tsx` is the third major block on the check detail page (e.g. `/dash0/orgs/default/checks/aa015625-07b1-44cf-a8e0-58ff44dc0545`). The component has **no `useTranslation` import** — every column header, period label, card title and empty-state value is a raw English literal. With language set to French/German/Spanish the whole table stays in English while the rest of the page translates.

The leaking strings are split between a const config (`PERIODS`) and the JSX:

| Lines | String | Context |
|---|---|---|
| 39, 45, 51, 57 | `"Today"` / `"Last 7 days"` / `"Last 30 days"` / `"Last 365 days"` | `PERIODS[i].label` (desktop) |
| 40, 46, 52, 58 | `"1d"` / `"7d"` / `"30d"` / `"1y"` | `PERIODS[i].shortLabel` (mobile) — keep as-is, universal |
| 258, 270 | `"Availability"` | `<CardTitle>` (rendered twice — once in the loading skeleton, once in the loaded state) |
| 276 | `"Time period"` | column header |
| 277 | `"Availability"` | column header |
| 278 | `"Downtime"` | column header |
| 279 | `"Incidents"` | column header |
| 280 | `"Longest incident"` | column header |
| 281 | `"Avg. incident"` | column header |
| 310, 313 | `"none"` | rendered when `row.incidents === 0` for the longest/average columns |

`formatDuration` (lines 79–90) emits `d`/`h`/`m`/`s` suffixes — leave those alone (universal abbreviations, also used by `check-summary-cards.tsx`).

## Scope

In scope:
- `web/dash0/src/components/checks/availability-table.tsx` — wire up `useTranslation`, move period labels out of the module-level `PERIODS` array (since `t()` is a hook, it must be called inside the component), and replace the JSX literals.
- `web/dash0/src/locales/{en,fr,de,es}/checks.json` — add a `detail.availability` block.

Out of scope:
- The `shortLabel` mobile abbreviations (`1d`, `7d`, `30d`, `1y`) — universal time-unit shorthand, no translation needed.
- `formatDuration` and `formatAvailability` helpers — emit unit suffixes / `%`, locale-independent.

## Translation keys

Add to `web/dash0/src/locales/en/checks.json` under `detail`:

```json
"availability": {
  "title": "Availability",
  "today": "Today",
  "last7": "Last 7 days",
  "last30": "Last 30 days",
  "last365": "Last 365 days",
  "timePeriod": "Time period",
  "availability": "Availability",
  "downtime": "Downtime",
  "incidents": "Incidents",
  "longestIncident": "Longest incident",
  "avgIncident": "Avg. incident",
  "none": "none"
}
```

`fr/checks.json`:

```json
"availability": {
  "title": "Disponibilité",
  "today": "Aujourd'hui",
  "last7": "7 derniers jours",
  "last30": "30 derniers jours",
  "last365": "365 derniers jours",
  "timePeriod": "Période",
  "availability": "Disponibilité",
  "downtime": "Indisponibilité",
  "incidents": "Incidents",
  "longestIncident": "Incident le plus long",
  "avgIncident": "Incident moyen",
  "none": "aucun"
}
```

`de/checks.json`:

```json
"availability": {
  "title": "Verfügbarkeit",
  "today": "Heute",
  "last7": "Letzte 7 Tage",
  "last30": "Letzte 30 Tage",
  "last365": "Letzte 365 Tage",
  "timePeriod": "Zeitraum",
  "availability": "Verfügbarkeit",
  "downtime": "Ausfallzeit",
  "incidents": "Vorfälle",
  "longestIncident": "Längster Vorfall",
  "avgIncident": "Durchschn. Vorfall",
  "none": "keiner"
}
```

`es/checks.json`:

```json
"availability": {
  "title": "Disponibilidad",
  "today": "Hoy",
  "last7": "Últimos 7 días",
  "last30": "Últimos 30 días",
  "last365": "Últimos 365 días",
  "timePeriod": "Período",
  "availability": "Disponibilidad",
  "downtime": "Tiempo de inactividad",
  "incidents": "Incidentes",
  "longestIncident": "Incidente más largo",
  "avgIncident": "Incidente medio",
  "none": "ninguno"
}
```

## Component changes

`web/dash0/src/components/checks/availability-table.tsx`:

1. Add at top:
   ```ts
   import { useTranslation } from "react-i18next";
   ```

2. Move `label` out of the module-level `PERIODS` array. The `getStart`/`durationMs`/`shortLabel` fields stay; replace `label: "Today"` etc. with a `labelKey` field that points into the i18n catalogue:
   ```ts
   interface PeriodConfig {
     id: PeriodId;
     labelKey: "today" | "last7" | "last30" | "last365";
     shortLabel: string;
     getStart: () => Date;
     durationMs: number;
   }

   const PERIODS: PeriodConfig[] = [
     { id: "today",   labelKey: "today",   shortLabel: "1d",  getStart: () => startOfDay(new Date()),  durationMs: Date.now() - startOfDay(new Date()).getTime() },
     { id: "last7",   labelKey: "last7",   shortLabel: "7d",  getStart: () => subDays(new Date(), 7),  durationMs: 7   * 24 * 60 * 60 * 1000 },
     { id: "last30",  labelKey: "last30",  shortLabel: "30d", getStart: () => subDays(new Date(), 30), durationMs: 30  * 24 * 60 * 60 * 1000 },
     { id: "last365", labelKey: "last365", shortLabel: "1y",  getStart: () => subDays(new Date(), 365), durationMs: 365 * 24 * 60 * 60 * 1000 },
   ];
   ```

3. In `AvailabilityTable`, near the top of the function body:
   ```ts
   const { t } = useTranslation("checks");
   ```

4. In the `useMemo` that builds `rows` (line 161 onwards), replace the `label: period.label` field with `labelKey: period.labelKey`. Keep the row shape otherwise unchanged.

5. In the loading branch (line 258) and loaded branch (line 270): replace `<CardTitle>Availability</CardTitle>` with `<CardTitle>{t("detail.availability.title")}</CardTitle>`.

6. Replace the six `<TableHead>` literals (lines 276–281):
   ```tsx
   <TableHead>{t("detail.availability.timePeriod")}</TableHead>
   <TableHead>{t("detail.availability.availability")}</TableHead>
   <TableHead>{t("detail.availability.downtime")}</TableHead>
   <TableHead>{t("detail.availability.incidents")}</TableHead>
   <TableHead>{t("detail.availability.longestIncident")}</TableHead>
   <TableHead>{t("detail.availability.avgIncident")}</TableHead>
   ```

7. Replace the row-label rendering (lines 296–299):
   ```tsx
   <TableCell className="font-medium">
     <span className="sm:hidden">{row.shortLabel}</span>
     <span className="hidden sm:inline">{t(`detail.availability.${row.labelKey}`)}</span>
   </TableCell>
   ```

8. Replace `"none"` (lines 310, 313):
   ```tsx
   {row.incidents > 0 ? formatDuration(row.longest) : t("detail.availability.none")}
   ```
   ```tsx
   {row.incidents > 0 ? formatDuration(row.average) : t("detail.availability.none")}
   ```

## Verification

1. `make dev-test` running on :4000.
2. Visit `http://localhost:4000/dash0/orgs/default/checks/aa015625-07b1-44cf-a8e0-58ff44dc0545` in each language:
   - **en:** card title `Availability`; row labels `Today` / `Last 7 days` / `Last 30 days` / `Last 365 days`; column headers `Time period`, `Availability`, `Downtime`, `Incidents`, `Longest incident`, `Avg. incident`; empty incident cells show `none`.
   - **fr:** `Disponibilité`; `Aujourd'hui` / `7 derniers jours` / `30 derniers jours` / `365 derniers jours`; `Période`, `Disponibilité`, `Indisponibilité`, `Incidents`, `Incident le plus long`, `Incident moyen`; empty cells `aucun`.
   - **de:** `Verfügbarkeit`; `Heute` / `Letzte 7 Tage` / `Letzte 30 Tage` / `Letzte 365 Tage`; headers translated; empty cells `keiner`.
   - **es:** `Disponibilidad`; `Hoy` / `Últimos 7 días` / `Últimos 30 días` / `Últimos 365 días`; headers translated; empty cells `ninguno`.
3. Resize the viewport below the `sm` breakpoint — the row labels should fall back to the universal short forms (`1d`, `7d`, `30d`, `1y`), unchanged across languages.
4. Open a check that has incidents in some periods and not others — confirm the `"none"` → localised text only renders for the empty rows.

## Acceptance criteria

- [ ] `web/dash0/src/components/checks/availability-table.tsx` contains zero hardcoded English text in JSX. The `PERIODS` const no longer carries display labels.
- [ ] All four locale files contain a complete `detail.availability` block (12 keys). Verify with `jq '.detail.availability | keys | length' web/dash0/src/locales/*/checks.json` returning `12` four times.
- [ ] `make build-dash0` and `make lint-dash` are green.
- [ ] Manual language-switch test on the bug-report URL confirms every visible string in the availability table is translated.
