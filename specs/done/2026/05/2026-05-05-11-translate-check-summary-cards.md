# Translate the check-detail summary cards

**Status:** todo
**Owner:** frontend (dash0)
**Severity:** medium — the three KPI tiles at the top of every check page render entirely in English regardless of the operator's chosen language.

## Problem

The first thing an operator sees on a check detail page (e.g. `/dash0/orgs/default/checks/aa015625-07b1-44cf-a8e0-58ff44dc0545`) is a row of three summary cards: **uptime/downtime**, **last checked**, and **incidents**. These cards are rendered by `web/dash0/src/components/checks/check-summary-cards.tsx`, which has **no `useTranslation` import at all** — every string is a raw English literal. Switch the language to French/German/Spanish via the flag dropdown and these three cards stay stubbornly in English while everything around them (header, configuration card, last result card, recent results table, dependencies) translates correctly.

The leaking strings (with line numbers, current code at `web/dash0/src/components/checks/check-summary-cards.tsx`):

| Line | String | Context |
|---|---|---|
| 58 | `"Currently up for"` | shown when `lastResult.status === "up"` |
| 58 | `"Currently down for"` | shown when status is `down`/`error`/`timeout` |
| 58 | `"Status"` | fallback label when status is neither |
| 65 | `"Unknown"` | shown when `lastStatusChange` is null |
| 75 | `"Last checked"` | label of the second card |
| 79 | `" ago"` | suffix appended to the live duration |
| 82 | `"Never"` | shown when `lastResult.timestamp` is null |
| 92 | `"Incidents"` | label of the third card |

## Scope

In scope:
- `web/dash0/src/components/checks/check-summary-cards.tsx` — wire up `useTranslation` and replace the eight hardcoded literals.
- `web/dash0/src/locales/{en,fr,de,es}/checks.json` — add the new keys under a `detail.summary` sub-section.

Out of scope:
- The `formatDuration` helper at lines 11–21 emits `d`/`h`/`m`/`s` suffixes. These are universal abbreviations also used elsewhere in the page (e.g. `availability-table.tsx`); leave them as-is. If we ever want fully-localised durations we'll do it in one cross-page sweep.
- The status string itself (`"up"`/`"down"`/`"timeout"`/...) is a machine code and is not translated anywhere else either — out of scope.

## Translation keys

Add a new `detail.summary` block to `web/dash0/src/locales/en/checks.json` (alongside the existing `detail` keys at line 199):

```json
"summary": {
  "currentlyUp": "Currently up for",
  "currentlyDown": "Currently down for",
  "statusFallback": "Status",
  "unknown": "Unknown",
  "lastChecked": "Last checked",
  "ago": "{{duration}} ago",
  "never": "Never",
  "incidents": "Incidents"
}
```

`fr/checks.json`:

```json
"summary": {
  "currentlyUp": "En ligne depuis",
  "currentlyDown": "Hors ligne depuis",
  "statusFallback": "Statut",
  "unknown": "Inconnu",
  "lastChecked": "Dernière vérification",
  "ago": "il y a {{duration}}",
  "never": "Jamais",
  "incidents": "Incidents"
}
```

`de/checks.json`:

```json
"summary": {
  "currentlyUp": "Aktuell online seit",
  "currentlyDown": "Aktuell offline seit",
  "statusFallback": "Status",
  "unknown": "Unbekannt",
  "lastChecked": "Zuletzt geprüft",
  "ago": "vor {{duration}}",
  "never": "Nie",
  "incidents": "Vorfälle"
}
```

`es/checks.json`:

```json
"summary": {
  "currentlyUp": "En línea desde hace",
  "currentlyDown": "Fuera de línea desde hace",
  "statusFallback": "Estado",
  "unknown": "Desconocido",
  "lastChecked": "Última comprobación",
  "ago": "hace {{duration}}",
  "never": "Nunca",
  "incidents": "Incidentes"
}
```

The `ago` key uses interpolation rather than a plain suffix because the natural French/Spanish/German ordering is *prefix* (`il y a 5m`, `hace 5m`, `vor 5m`), not the English-style trailing `" ago"`. Building the string with `t("detail.summary.ago", { duration: formatDuration(elapsed) })` keeps each locale grammatical.

## Component changes

`web/dash0/src/components/checks/check-summary-cards.tsx`:

1. Add the hook import at the top of the file:
   ```ts
   import { useTranslation } from "react-i18next";
   ```
2. Inside `CheckSummaryCards`, near the top of the function body (before `const isUp = …`):
   ```ts
   const { t } = useTranslation("checks");
   ```
3. Replace line 58:
   ```tsx
   {isUp
     ? t("detail.summary.currentlyUp")
     : isDown
       ? t("detail.summary.currentlyDown")
       : t("detail.summary.statusFallback")}
   ```
4. Replace line 65 (`"Unknown"`):
   ```tsx
   {t("detail.summary.unknown")}
   ```
5. Replace lines 73–84 (label + duration with `ago` suffix). The cleanest rewrite, given the `ago` key now uses interpolation, is to fold `LiveDuration` into a render-prop or expose its computed string. Easiest local change: hoist the elapsed-ms computation up so the parent can call `t("detail.summary.ago", { duration: formatDuration(...) })`. Sketch:
   ```tsx
   <div className="flex items-center gap-2 text-sm text-muted-foreground mb-1">
     <Clock className="h-4 w-4" />
     {t("detail.summary.lastChecked")}
   </div>
   <div className="text-2xl font-bold">
     {check.lastResult?.timestamp ? (
       <LiveDurationAgo since={check.lastResult.timestamp} />
     ) : (
       t("detail.summary.never")
     )}
   </div>
   ```
   where `LiveDurationAgo` is a small variant of `LiveDuration` that wraps the duration in the localised template:
   ```tsx
   function LiveDurationAgo({ since }: { since: string }) {
     const { t } = useTranslation("checks");
     const [now, setNow] = useState(() => Date.now());
     useEffect(() => {
       const interval = setInterval(() => setNow(Date.now()), 1000);
       return () => clearInterval(interval);
     }, []);
     const elapsed = Math.max(0, now - new Date(since).getTime());
     return <>{t("detail.summary.ago", { duration: formatDuration(elapsed) })}</>;
   }
   ```
   Reuse `LiveDuration` unchanged for the first card (no `ago` suffix there).
6. Replace line 92:
   ```tsx
   {t("detail.summary.incidents")}
   ```

## Verification

1. `make dev-test` is the standard dev loop per CLAUDE.md — keep it running on port 4000 so the bundler picks up the JSON edits and the component change.
2. Open `http://localhost:4000/dash0/orgs/default/checks/aa015625-07b1-44cf-a8e0-58ff44dc0545` (the email check from the bug report) and:
   - With language = English: the three card labels read `Currently up for` / `Last checked` / `Incidents`. Behaviour identical to today.
   - Switch language to French via the flag dropdown: labels become `En ligne depuis` / `Dernière vérification` / `Incidents`; the live-duration line under "Dernière vérification" reads `il y a 5m 12s` (or similar), not `5m 12s ago`.
   - Repeat for German (`Aktuell online seit` / `Zuletzt geprüft` / `Vorfälle`) and Spanish (`En línea desde hace` / `Última comprobación` / `Incidentes`).
3. Open a check that has never produced a result (or stub `lastResult: null` in DevTools) — the second card should show the localised `Jamais` / `Nie` / `Nunca` instead of `Never`.

## Acceptance criteria

- [ ] `web/dash0/src/components/checks/check-summary-cards.tsx` contains zero hardcoded English strings (grep `Currently\|Last checked\|Never\|Unknown\|Incidents\|"Status"\|" ago"` against the file returns nothing inside JSX text nodes).
- [ ] All four locale files (`en`, `fr`, `de`, `es`) contain a complete `detail.summary` block with the eight keys above. Counts match across the four files (verify with `jq '.detail.summary | keys | length' web/dash0/src/locales/*/checks.json` returning `8` four times).
- [ ] `make build-dash0` and `make lint-dash` are green.
- [ ] Manual language-switch test on the bug-report URL confirms the three cards translate cleanly in all four languages.
