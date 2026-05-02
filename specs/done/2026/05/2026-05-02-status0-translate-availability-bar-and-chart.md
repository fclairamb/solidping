# Status0 — translate availability bar and response time chart strings

## Context

The public status page at `web/status0` (e.g. `http://localhost:4000/status0/default/test?lang=fr`) has partial i18n. Header, status badges, footer "Powered by", and section/empty-state messages are translated through `react-i18next`. **But the per-resource availability bar and the response-time chart still render hardcoded English strings**, so French/German/Spanish visitors see a mix of their language and English on the same page.

User report (with screenshot, lang=fr):

> Everything should be localized not just the header and footer

The strings that leak through are visible right under the green "Tous les systèmes sont opérationnels" badge:

- `90 days ago` (left edge of availability bar)
- `100.000% uptime` (centered under the bar)
- `Today` (right edge of the bar)
- `Response Time` (chart title)

Two more leak through tooltips that aren't visible in the screenshot but are equally affected:

- `% uptime` suffix in the per-day hover tooltip on the availability bar
- `No data` in both the availability-bar tooltip (days with no measurements) and the response-time chart tooltip

## Scope

This spec covers **only** the hardcoded English strings. A sibling spec (`2026-05-02-status0-locale-aware-date-formatting.md`) handles the related issue that `toLocaleDateString(undefined, …)` calls in the same two files render dates in the browser locale instead of the i18n-active language. The two specs touch the same files but solve independent problems and can be implemented in either order.

Out of scope:
- The `udp`/`http`/`tcp` etc. protocol badge under each resource name (line ~113 of `status-page-view.tsx`). Those are protocol identifiers, not English words — leave untranslated.
- User-supplied content (`page.name`, `page.description`, `section.name`, `resource.publicName`). Already correctly *not* translated.
- The dash0 admin app — separate i18n surface, separate spec history.

## Files to change

### 1. Translation JSON files

Add the new keys to all four locale files. Existing keys stay untouched.

**`web/status0/src/locales/en/status.json`**

Add:

```json
"daysAgo": "{{count}} days ago",
"today": "Today",
"uptime": "uptime",
"noData": "No data",
"responseTime": "Response Time"
```

**`web/status0/src/locales/fr/status.json`**

Add:

```json
"daysAgo": "il y a {{count}} jours",
"today": "Aujourd'hui",
"uptime": "de disponibilité",
"noData": "Aucune donnée",
"responseTime": "Temps de réponse"
```

Notes on the French wording:
- `"de disponibilité"` is the natural French rendering of "uptime" when it follows a percentage (`100.000 % de disponibilité`). Avoid the borrowed `uptime` — French status pages from OVH/Scaleway use `disponibilité`.
- `"Aujourd'hui"` keeps the apostrophe; the JSON encoder handles it.

**`web/status0/src/locales/de/status.json`**

Add:

```json
"daysAgo": "vor {{count}} Tagen",
"today": "Heute",
"uptime": "Verfügbarkeit",
"noData": "Keine Daten",
"responseTime": "Antwortzeit"
```

**`web/status0/src/locales/es/status.json`**

Add:

```json
"daysAgo": "hace {{count}} días",
"today": "Hoy",
"uptime": "de disponibilidad",
"noData": "Sin datos",
"responseTime": "Tiempo de respuesta"
```

### 2. `web/status0/src/components/shared/availability-bar.tsx`

Currently the file has no `useTranslation` import. Add one and replace four English strings.

Top of file — add the hook import:

```ts
import { useTranslation } from "react-i18next";
```

Inside `AvailabilityBar`, near the top of the function body:

```ts
const { t } = useTranslation();
```

Replace the tooltip body (lines ~52–58):

```tsx
{point.status !== "noData" ? (
  <p className="text-xs">
    {point.availabilityPct.toFixed(2)}% {t("uptime")}
  </p>
) : (
  <p className="text-xs text-muted-foreground">{t("noData")}</p>
)}
```

Replace the footer row (lines ~63–71):

```tsx
<div className="mt-1 flex justify-between text-xs text-muted-foreground">
  <span>{t("daysAgo", { count: historyDays })}</span>
  {overallAvailabilityPct != null && (
    <span className="font-medium text-foreground">
      {overallAvailabilityPct.toFixed(3)}% {t("uptime")}
    </span>
  )}
  <span>{t("today")}</span>
</div>
```

Note: i18next's `count` interpolation is used for the `daysAgo` plural form. None of the four target languages currently need separate singular/plural keys for "90 days" (the historyDays value is always plural in practice — a status page configured with `historyDays: 1` would show `1 days ago` in English, which is mildly awkward but not a regression vs. the current state). If singular forms are needed later, switch to i18next plural keys (`daysAgo_one`/`daysAgo_other`).

### 3. `web/status0/src/components/shared/response-time-chart.tsx`

Currently the file has no `useTranslation` import. Add one. The component has two consumer surfaces: the `CustomTooltip` subcomponent (uses `No data`) and the chart title (`Response Time`). Both need `t()` access.

Add at top:

```ts
import { useTranslation } from "react-i18next";
```

`CustomTooltip` is currently a top-level function, but `useTranslation` is a hook so it must be called from inside the component. Easiest fix: keep `CustomTooltip` as a regular component and call the hook inside it. Replace the existing function signature and `No data` line:

```tsx
function CustomTooltip({
  active,
  payload,
}: {
  active?: boolean;
  payload?: Array<{ payload: ResponseTimePoint }>;
}) {
  const { t } = useTranslation();
  if (!active || !payload?.length) return null;
  const data = payload[0].payload;
  const date = new Date(data.time);
  return (
    <div className="rounded-md border bg-background px-3 py-2 text-sm shadow-md">
      <p className="font-medium">
        {date.toLocaleDateString(undefined, {
          month: "short",
          day: "numeric",
        })}{" "}
        {date.toLocaleTimeString(undefined, {
          hour: "numeric",
          minute: "2-digit",
        })}
      </p>
      {data.durationP95 != null ? (
        <p className="text-xs text-muted-foreground">
          {formatDuration(data.durationP95)}
        </p>
      ) : (
        <p className="text-xs text-muted-foreground">{t("noData")}</p>
      )}
    </div>
  );
}
```

(The `toLocaleDateString(undefined, …)` calls remain untouched in this spec — they belong to the sibling spec.)

In `ResponseTimeChart`, near the top of the function body:

```ts
const { t } = useTranslation();
```

Replace line 84:

```tsx
<p className="mb-1 text-xs text-muted-foreground">{t("responseTime")}</p>
```

## Verification

1. `make build-status0` — confirms no TypeScript errors after the hook additions.
2. `make dev-test` is already running on port 4000 per CLAUDE.md. Open in browser:
   - `http://localhost:4000/status0/default/test?lang=en` — expect `90 days ago`, `Today`, `% uptime`, `Response Time`, tooltips show `No data` for days without measurements.
   - `http://localhost:4000/status0/default/test?lang=fr` — expect `il y a 90 jours`, `Aujourd'hui`, `% de disponibilité`, `Temps de réponse`, tooltips show `Aucune donnée`.
   - `?lang=de` — expect `vor 90 Tagen`, `Heute`, `Verfügbarkeit`, `Antwortzeit`, `Keine Daten`.
   - `?lang=es` — expect `hace 90 días`, `Hoy`, `de disponibilidad`, `Tiempo de respuesta`, `Sin datos`.
3. Hover the day-cells in the availability bar to confirm the tooltip text translates (the screenshot only showed the bar's footer row).
4. Toggle language via the flag dropdown without reloading — the strings should switch immediately because `useTranslation` subscribes to language change.

## Final grep check

After the change, this should return zero matches in `web/status0/src/`:

```bash
rtk grep -rn '"Today"\|"No data"\|"Response Time"\|days ago\|% uptime' web/status0/src/components/
```

(The literal `"Today"` etc. should now exist only in the locale JSON files.)

---

## Implementation Plan

1. Add `daysAgo`, `today`, `uptime`, `noData`, `responseTime` keys to the four `web/status0/src/locales/{en,fr,de,es}/status.json` files.
2. In `availability-bar.tsx`, add `useTranslation`; replace the four hardcoded English strings (tooltip uptime + noData, footer daysAgo + today + uptime).
3. In `response-time-chart.tsx`, add `useTranslation` to both `CustomTooltip` and `ResponseTimeChart`; replace `No data` and `Response Time`.
4. `make build-status0 lint-dash` clean.
