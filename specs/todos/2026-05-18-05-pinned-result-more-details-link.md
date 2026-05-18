# Pinned result box — replace "Click again" with an explicit "More details" link

## Context

The `PinnedResultBox` component
(`web/dash0/src/components/checks/pinned-result-box.tsx:160–162`) shows a
footer hint:

```
Click again to open full page
```

Navigation to the full result page currently requires knowing to click the
same chart dot a second time. That interaction is:

- **Hidden** — users who miss the hint dismiss the box and never discover the full page.
- **Ambiguous** — a click on an already-selected dot looks like a mis-click; the user has to remember a special rule.

The intent of the original two-step design (spec
`specs/done/2026/05/2026-05-04-02-chart-point-preview-then-open.md`) was
correct: show a lightweight preview without forcing a route change. The mistake
was hiding the "go deeper" action behind a gesture instead of an explicit link.

Additionally, the dot's SVG `<title>` inside `response-time-chart.tsx:655–659`
also advertises "Click again to open full page" when the dot is selected, so
the affordance lives in two places and both need updating.

## Goal

- Replace the bottom hint text in `PinnedResultBox` with a TanStack Router
  `<Link>` styled as a link-button reading **"More details →"**.
- Make second-click-on-same-dot **dismiss the preview** instead of navigating,
  so click semantics are unambiguous: click opens, click again closes.
- Remove the i18n key `detail.resultBox.clickAgain` and its chart counterpart
  `detail.chart.dotClickAgain` from all four locale files (the only consumers
  are gone). Optionally add `detail.chart.dotClickToClose` for the dot `<title>`
  in the selected state.

## Scope

### `web/dash0/src/components/checks/pinned-result-box.tsx`

Replace lines 160–162:

```tsx
<p className="mt-2 text-xs text-muted-foreground">
  {t("detail.resultBox.clickAgain")}
</p>
```

with:

```tsx
<div className="mt-2">
  <Button asChild variant="link" size="sm" className="h-auto p-0 text-xs">
    <Link
      to="/orgs/$org/checks/$checkUid/results/$resultUid"
      params={{ org, checkUid, resultUid }}
    >
      {t("detail.resultBox.moreDetails")}
    </Link>
  </Button>
</div>
```

Add `import { Link } from "@tanstack/react-router"` (alongside the existing
`Button` import). `org`, `checkUid`, `resultUid` are already props.

### `web/dash0/src/components/checks/response-time-chart.tsx`

`handleDotClick` currently navigates on a second click to the same dot:

```ts
if (selectedUid === uid) {
  navigate({ to: …/results/$resultUid, params: … });
  return;
}
setSelectedUid(uid);
```

Replace the "if same" branch to dismiss instead:

```ts
if (selectedUid === uid) {
  setSelectedUid(null);
  return;
}
setSelectedUid(uid);
```

At lines 655–659, the dot `<title>` reads `t("detail.chart.dotClickAgain")`
when selected. Change it to `t("detail.chart.dotClickToClose")` ("Click to
close") or simply reuse `t("detail.chart.dotClickForDetails")` — either way,
remove the "open full page" promise from the tooltip.

### `web/dash0/src/locales/{en,fr,de,es}/checks.json`

- **Add** `detail.resultBox.moreDetails`: `"More details →"` (en); translate fr/de/es.
- **Remove** `detail.resultBox.clickAgain` (line 242 in en).
- **Remove** `detail.chart.dotClickAgain` (line 231 in en).
- **Add** `detail.chart.dotClickToClose`: `"Click to close"` (en) if used above.

### `web/dash0/e2e/check-chart-point-preview.spec.ts`

Update the assertion that previously tested "click same dot → URL changes to
`**/results/**`":

- Replace with: **clicking the "More details" link navigates** to `**/results/**`.
- Add: **second click on the same dot dismisses the preview** (box disappears, URL unchanged).

## Acceptance criteria

- [ ] The pinned result box shows a **"More details →"** link at the bottom that navigates to the full result page when clicked.
- [ ] The bottom hint text "Click again to open full page" is gone.
- [ ] Clicking a selected dot a second time **closes** the preview without navigating.
- [ ] The chart dot `<title>` no longer reads "Click again to open full page" in any state.
- [ ] Keys `detail.resultBox.clickAgain` and `detail.chart.dotClickAgain` are removed from all four locale files.
- [ ] Playwright e2e in `check-chart-point-preview.spec.ts` passes with the updated assertions.
- [ ] `make test-dash` green; `make lint` clean.
